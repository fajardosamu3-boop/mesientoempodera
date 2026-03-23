package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// ──────────────────────────────────────────
//  Tipos
// ──────────────────────────────────────────

type Status string

const (
	StatusValid   Status = "VALID"
	StatusInvalid Status = "INVALID"
	StatusUnknown Status = "UNKNOWN"
)

type CheckoutInfo struct {
	URL         string `json:"url"`
	Status      Status `json:"status"`
	Price       string `json:"price,omitempty"`
	InvoiceID   string `json:"invoice_id,omitempty"`
	OrderStatus string `json:"order_status,omitempty"`
	LastChecked string `json:"last_checked"`
}

// ──────────────────────────────────────────
//  Configuración
// ──────────────────────────────────────────

const (
	inputFile   = "targets.txt"
	jsonFile    = "checkout.json"
	unknownFile = "unknown.txt"
	invalidFile = "invalid.txt"
	checkDelay  = 30 * time.Second
	pageTimeout = 25 * time.Second
	maxWorkers  = 2 // chromedp es pesado
)

// Señales en el body de la página renderizada
var invalidBodySignals = []string{
	"factura no encontrada",
	"invoice not found",
	"no encontrada",
	"not found",
	"checkout expired",
}

var validBodySignals = []string{
	"su pedido se ha realizado correctamente",
	"order has been placed",
	"entregado",
	"delivered",
	"thank you for shopping",
	"gracias por",
}

// Señales en respuestas JSON de red interceptadas
var invalidAPISignals = []string{
	`"not_found"`, `"VOIDED"`, `"CANCELLED"`, `"EXPIRED"`,
	`"error"`, `"not found"`, `"invalid"`,
}

var validAPISignals = []string{
	`"COMPLETED"`, `"completed"`, `"PAID"`, `"paid"`,
	`"delivered"`, `"DELIVERED"`,
}

// ──────────────────────────────────────────
//  Estado global
// ──────────────────────────────────────────

var (
	seen sync.Map

	bufMu         sync.Mutex
	validBuffer   []CheckoutInfo
	invalidBuffer []string
	unknownBuffer []string

	fileMu sync.Mutex
)

// ──────────────────────────────────────────
//  Main
// ──────────────────────────────────────────

func main() {
	fmt.Println("[START] Checkout Monitor (chromedp) – VALID / INVALID / UNKNOWN")

	for {
		seen = sync.Map{}

		targets, err := loadLines(inputFile)
		if err != nil {
			fmt.Println("[ERR] leyendo targets:", err)
			time.Sleep(10 * time.Second)
			continue
		}

		sem := make(chan struct{}, maxWorkers)
		var wg sync.WaitGroup

		for _, u := range targets {
			u := u
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				checkURL(u)
			}()
		}

		wg.Wait()
		flushBuffers()
		fmt.Printf("[WAIT] Ciclo completado. Siguiente en %s\n", checkDelay)
		time.Sleep(checkDelay)
	}
}

// ──────────────────────────────────────────
//  Check con chromedp + intercepción de red
// ──────────────────────────────────────────

func checkURL(rawURL string) {
	if !strings.HasPrefix(rawURL, "http") {
		rawURL = "https://" + rawURL
	}
	if _, loaded := seen.LoadOrStore(rawURL, true); loaded {
		return
	}

	invoiceID := extractInvoiceID(rawURL)
	now := time.Now().Format(time.RFC3339)

	// Opciones headless — funciona en Linux sin display
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()

	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	defer cancelCtx()

	ctx, cancelTimeout := context.WithTimeout(ctx, pageTimeout)
	defer cancelTimeout()

	// Mapa para capturar respuestas de red
	// key = requestID, value = responseBody string
	var netMu sync.Mutex
	capturedBodies := make(map[network.RequestID]string)
	capturedURLs := make(map[network.RequestID]string)

	// Escuchar eventos de red ANTES de navegar
	chromedp.ListenTarget(ctx, func(ev interface{}) {
		switch e := ev.(type) {

		case *network.EventResponseReceived:
			url := e.Response.URL
			// Solo nos interesan llamadas a APIs (JSON), no assets
			if strings.Contains(url, "/api/") ||
				strings.Contains(url, "sellix") ||
				strings.Contains(url, "invoice") ||
				strings.Contains(url, "order") ||
				strings.Contains(url, "checkout") {
				netMu.Lock()
				capturedURLs[e.RequestID] = url
				netMu.Unlock()
				fmt.Printf("[NET] %s → HTTP %d | %s\n", invoiceID, e.Response.Status, url)
			}

		case *network.EventLoadingFinished:
			netMu.Lock()
			url, ok := capturedURLs[e.RequestID]
			netMu.Unlock()
			if !ok {
				return
			}
			// Obtener el body de esta respuesta en una goroutine
			go func(reqID network.RequestID, reqURL string) {
				// Necesitamos un contexto nuevo derivado (no el cancelado)
				getCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()

				var body []byte
				err := chromedp.Run(getCtx,
					chromedp.ActionFunc(func(c context.Context) error {
						var err error
						body, err = network.GetResponseBody(reqID).Do(c)
						return err
					}),
				)
				if err != nil {
					return
				}

				bodyStr := string(body)
				if len(bodyStr) > 500 {
					fmt.Printf("[API]  %s → %q...\n", reqURL, bodyStr[:500])
				} else {
					fmt.Printf("[API]  %s → %q\n", reqURL, bodyStr)
				}

				netMu.Lock()
				capturedBodies[reqID] = bodyStr
				netMu.Unlock()
			}(e.RequestID, url)
		}
	})

	// Navegar y esperar a que el JS renderice
	var bodyText string
	err := chromedp.Run(ctx,
		network.Enable(),
		chromedp.Navigate(rawURL),
		chromedp.Sleep(4*time.Second), // esperar renderizado JS
		chromedp.Text("body", &bodyText, chromedp.ByQuery),
	)

	if err != nil {
		fmt.Printf("[DEAD]    %s | %v\n", rawURL, err)
		markUnknown(rawURL, "error cargando página")
		return
	}

	// Dar tiempo a que terminen las goroutines de captura
	time.Sleep(1 * time.Second)

	bodyLower := strings.ToLower(bodyText)
	preview := bodyLower
	if len(preview) > 400 {
		preview = preview[:400]
	}
	fmt.Printf("[PAGE] %s\n  body: %q\n", rawURL, preview)

	// ── 1. Analizar respuestas de API capturadas ──────────────
	netMu.Lock()
	bodies := make([]string, 0, len(capturedBodies))
	for _, b := range capturedBodies {
		bodies = append(bodies, b)
	}
	netMu.Unlock()

	for _, apiBody := range bodies {
		apiLower := strings.ToLower(apiBody)

		// Primero señales INVALID en API
		for _, sig := range invalidAPISignals {
			if strings.Contains(apiLower, sig) {
				// Verificar que no haya también señal válida (falso positivo)
				hasValid := false
				for _, vs := range validAPISignals {
					if strings.Contains(apiLower, vs) {
						hasValid = true
						break
					}
				}
				if !hasValid {
					markInvalid(rawURL, fmt.Sprintf("API signal: %s", sig))
					return
				}
			}
		}

		// Señales VALID en API
		for _, sig := range validAPISignals {
			if strings.Contains(apiLower, sig) {
				price := extractPrice(apiBody)
				info := CheckoutInfo{
					URL:         rawURL,
					Status:      StatusValid,
					Price:       price,
					InvoiceID:   invoiceID,
					OrderStatus: sig,
					LastChecked: now,
				}
				bufMu.Lock()
				validBuffer = append(validBuffer, info)
				bufMu.Unlock()
				fmt.Printf("[VALID]   %s | API signal: %s | precio: %s\n", rawURL, sig, price)
				return
			}
		}
	}

	// ── 2. Analizar body de la página renderizada ─────────────
	for _, sig := range invalidBodySignals {
		if strings.Contains(bodyLower, sig) {
			markInvalid(rawURL, fmt.Sprintf("page signal: %q", sig))
			return
		}
	}

	for _, sig := range validBodySignals {
		if strings.Contains(bodyLower, sig) {
			price := extractPrice(bodyText)
			info := CheckoutInfo{
				URL:         rawURL,
				Status:      StatusValid,
				Price:       price,
				InvoiceID:   invoiceID,
				LastChecked: now,
			}
			bufMu.Lock()
			validBuffer = append(validBuffer, info)
			bufMu.Unlock()
			fmt.Printf("[VALID]   %s | page signal: %q | precio: %s\n", rawURL, sig, price)
			return
		}
	}

	markUnknown(rawURL, "sin señales claras tras renderizado JS")
}

// ──────────────────────────────────────────
//  Helpers
// ──────────────────────────────────────────

func extractInvoiceID(rawURL string) string {
	parts := strings.Split(strings.TrimRight(rawURL, "/"), "/")
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		if last != "" {
			return last
		}
	}
	return ""
}

func extractPrice(body string) string {
	for _, ind := range []string{"€", "$", "£"} {
		idx := strings.Index(body, ind)
		if idx == -1 {
			continue
		}
		end := idx + 15
		if end > len(body) {
			end = len(body)
		}
		snippet := strings.TrimSpace(body[idx:end])
		if i := strings.IndexAny(snippet, " \n\r\t,}\""); i > 0 {
			snippet = snippet[:i]
		}
		if len(snippet) > 1 {
			return snippet
		}
	}
	return ""
}

func markInvalid(rawURL, reason string) {
	bufMu.Lock()
	invalidBuffer = append(invalidBuffer, rawURL)
	bufMu.Unlock()
	fmt.Printf("[INVALID] %s | %s\n", rawURL, reason)
}

func markUnknown(rawURL, reason string) {
	bufMu.Lock()
	unknownBuffer = append(unknownBuffer, rawURL)
	bufMu.Unlock()
	fmt.Printf("[UNKNOWN] %s | %s\n", rawURL, reason)
}

// ──────────────────────────────────────────
//  Flush a archivos
// ──────────────────────────────────────────

func flushBuffers() {
	bufMu.Lock()
	vb := validBuffer
	ib := invalidBuffer
	ub := unknownBuffer
	validBuffer = nil
	invalidBuffer = nil
	unknownBuffer = nil
	bufMu.Unlock()

	if len(vb) > 0 {
		appendJSON(jsonFile, vb)
		fmt.Printf("[FLUSH] %d válidos → %s\n", len(vb), jsonFile)
	}
	if len(ib) > 0 {
		appendLines(invalidFile, ib)
		fmt.Printf("[FLUSH] %d inválidos → %s\n", len(ib), invalidFile)
	}
	if len(ub) > 0 {
		appendLines(unknownFile, ub)
		fmt.Printf("[FLUSH] %d desconocidos → %s\n", len(ub), unknownFile)
	}
}

// ──────────────────────────────────────────
//  Archivos
// ──────────────────────────────────────────

func loadLines(file string) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return lines, sc.Err()
}

func appendLines(file string, lines []string) {
	fileMu.Lock()
	defer fileMu.Unlock()

	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println("[ERR] abriendo", file, err)
		return
	}
	defer f.Close()
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
}

func appendJSON(file string, newData []CheckoutInfo) {
	fileMu.Lock()
	defer fileMu.Unlock()

	var all []CheckoutInfo
	if f, err := os.Open(file); err == nil {
		json.NewDecoder(f).Decode(&all)
		f.Close()
	}
	all = append(all, newData...)

	out, err := os.Create(file)
	if err != nil {
		fmt.Println("[ERR] creando", file, err)
		return
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.Encode(all)
}
