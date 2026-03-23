package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
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
	Method      string `json:"payment_method"`
	Price       string `json:"price,omitempty"`
	InvoiceID   string `json:"invoice_id,omitempty"`
	OrderStatus string `json:"order_status,omitempty"`
	LastChecked string `json:"last_checked"`
}

type Config struct {
	APIKey      string `json:"api_key"`
	BearerToken string `json:"bearer_token"`
}

// Respuesta de Sellix API
type SellixResponse struct {
	Status int `json:"status"`
	Data   struct {
		Invoice struct {
			Uniqid       string  `json:"uniqid"`
			Status       string  `json:"status"`
			TotalDisplay float64 `json:"total_display"`
			Currency     string  `json:"currency"`
			Gateway      string  `json:"gateway"`
		} `json:"invoice"`
		Order struct {
			Uniqid       string  `json:"uniqid"`
			Status       string  `json:"status"`
			TotalDisplay float64 `json:"total_display"`
			Currency     string  `json:"currency"`
			Gateway      string  `json:"gateway"`
		} `json:"order"`
	} `json:"data"`
	Error string `json:"error"`
}

// ──────────────────────────────────────────
//  Configuración
// ──────────────────────────────────────────

const (
	inputFile   = "targets.txt"
	jsonFile    = "checkout.json"
	unknownFile = "unknown.txt"
	invalidFile = "invalid.txt"
	proxiesFile = "proxies.txt"
	configFile  = "config.json"
	checkDelay  = 30 * time.Second
	httpTimeout = 12 * time.Second
	maxWorkers  = 10
)

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

	proxies []string
	cfg     Config
)

// ──────────────────────────────────────────
//  Main
// ──────────────────────────────────────────

func main() {
	fmt.Println("[START] Checkout Monitor – VALID / INVALID / UNKNOWN")

	loadConfig()
	loadProxies()

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
//  HTTP client
// ──────────────────────────────────────────

func newClient() *http.Client {
	transport := &http.Transport{}

	if len(proxies) > 0 {
		proxy := proxies[rand.Intn(len(proxies))]
		if proxyURL, err := url.Parse(proxy); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}

	return &http.Client{
		Timeout:   httpTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("demasiados redirects")
			}
			return nil
		},
	}
}

// ──────────────────────────────────────────
//  Extrae el invoice ID de la URL
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

// ──────────────────────────────────────────
//  Hace un GET y devuelve body + status code
// ──────────────────────────────────────────

func doGet(client *http.Client, rawURL string, headers map[string]string) ([]byte, int, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, err
}

// ──────────────────────────────────────────
//  Check de una URL
// ──────────────────────────────────────────

func checkURL(rawURL string) {
	if !strings.HasPrefix(rawURL, "http") {
		rawURL = "https://" + rawURL
	}

	if _, loaded := seen.LoadOrStore(rawURL, true); loaded {
		return
	}

	invoiceID := extractInvoiceID(rawURL)
	if invoiceID == "" {
		markUnknown(rawURL, "no se pudo extraer invoice ID")
		return
	}

	client := newClient()
	now := time.Now().Format(time.RFC3339)

	baseHeaders := map[string]string{
		"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		"Accept":     "application/json",
		"Referer":    rawURL,
	}

	// Añadir auth si está configurada
	if cfg.BearerToken != "" && cfg.BearerToken != "AQUI_PON_TU_TOKEN_SI_ES_BEARER" {
		baseHeaders["Authorization"] = "Bearer " + cfg.BearerToken
	} else if cfg.APIKey != "" && cfg.APIKey != "AQUI_PON_TU_API_KEY" {
		baseHeaders["Authorization"] = "Bearer " + cfg.APIKey
	}

	// ── 1. Sellix API (backend de ResellMe) ──────────────────
	// ResellMe corre sobre Sellix. El endpoint público es:
	// GET https://dev.sellix.io/v1/orders/{uniqid}
	// Sin auth devuelve 401, pero con la API key del vendedor devuelve el estado.
	// Sin API key intentamos de todas formas — algunos shops tienen órdenes públicas.
	sellixURLs := []string{
		fmt.Sprintf("https://dev.sellix.io/v1/orders/%s", invoiceID),
		fmt.Sprintf("https://dev.sellix.io/v1/payments/%s", invoiceID),
	}

	for _, apiURL := range sellixURLs {
		body, code, err := doGet(client, apiURL, baseHeaders)
		if err != nil {
			continue
		}

		if code == 404 {
			markInvalid(rawURL, fmt.Sprintf("Sellix API 404: factura no existe (%s)", apiURL))
			return
		}

		if code == 200 {
			status, gateway, price, currency := parseSellixBody(body)
			if status != "" {
				handleSellixStatus(rawURL, invoiceID, status, gateway, price, currency, now)
				return
			}
		}
		// 401/403 = necesita auth → seguir con siguiente método
	}

	// ── 2. ResellMe API interna ───────────────────────────────
	resellmeURLs := []string{
		fmt.Sprintf("https://resellme.xyz/api/invoices/%s", invoiceID),
		fmt.Sprintf("https://resellme.xyz/api/orders/%s", invoiceID),
		fmt.Sprintf("https://resellme.xyz/api/checkout/%s", invoiceID),
	}

	for _, apiURL := range resellmeURLs {
		hdrs := map[string]string{
			"User-Agent":        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
			"Accept":            "application/json",
			"Referer":           rawURL,
			"X-Requested-With": "XMLHttpRequest",
		}
		body, code, err := doGet(client, apiURL, hdrs)
		if err != nil {
			continue
		}
		bodyLower := strings.ToLower(string(body))

		if code == 404 {
			markInvalid(rawURL, "ResellMe API: no encontrado")
			return
		}

		if code == 200 {
			if strings.Contains(bodyLower, `"completed"`) || strings.Contains(bodyLower, `"paid"`) ||
				strings.Contains(bodyLower, "entregado") || strings.Contains(bodyLower, "delivered") {
				bufMu.Lock()
				validBuffer = append(validBuffer, CheckoutInfo{
					URL: rawURL, Status: StatusValid,
					Method: "Resellme", InvoiceID: invoiceID, LastChecked: now,
				})
				bufMu.Unlock()
				fmt.Printf("[VALID]   %s | ResellMe API positiva\n", rawURL)
				return
			}
			if strings.Contains(bodyLower, "no encontrada") || strings.Contains(bodyLower, "not found") ||
				strings.Contains(bodyLower, "invalid") {
				markInvalid(rawURL, "ResellMe API: factura no encontrada")
				return
			}
		}
	}

	// ── 3. Fallback: GET página HTML y buscar señales SSR ─────
	htmlHdrs := map[string]string{
		"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "es-ES,es;q=0.9,en;q=0.8",
	}
	body, code, err := doGet(client, rawURL, htmlHdrs)
	if err != nil {
		fmt.Printf("[DEAD]    %s | %v\n", rawURL, err)
		return
	}

	if code == 404 || code == 410 {
		markInvalid(rawURL, fmt.Sprintf("HTTP %d", code))
		return
	}

	bodyLower := strings.ToLower(string(body))

	negatives := []string{"factura no encontrada", "invoice not found", "order not found", "checkout expired"}
	for _, sig := range negatives {
		if strings.Contains(bodyLower, sig) {
			markInvalid(rawURL, fmt.Sprintf("señal HTML: %q", sig))
			return
		}
	}

	positives := []string{"completed", "entregado", "su pedido se ha realizado", "order completed", "paid"}
	for _, sig := range positives {
		if strings.Contains(bodyLower, sig) {
			bufMu.Lock()
			validBuffer = append(validBuffer, CheckoutInfo{
				URL: rawURL, Status: StatusValid,
				Method: "Resellme", InvoiceID: invoiceID, LastChecked: now,
			})
			bufMu.Unlock()
			fmt.Printf("[VALID]   %s | señal HTML: %q\n", rawURL, sig)
			return
		}
	}

	// SPA pura — no se puede determinar sin JS
	// SOLUCIÓN: pon tu API key de Sellix (del vendedor) en config.json como bearer_token
	markUnknown(rawURL, "SPA pura. SOLUCIÓN: pon la API key de Sellix del vendedor en config.json como bearer_token")
}

// ──────────────────────────────────────────
//  Parsea respuesta Sellix y devuelve campos clave
// ──────────────────────────────────────────

func parseSellixBody(body []byte) (status, gateway, price, currency string) {
	var result SellixResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return
	}
	// Puede estar en .data.invoice o .data.order
	if result.Data.Invoice.Status != "" {
		inv := result.Data.Invoice
		status = strings.ToUpper(inv.Status)
		gateway = inv.Gateway
		price = fmt.Sprintf("%.2f", inv.TotalDisplay)
		currency = inv.Currency
	} else if result.Data.Order.Status != "" {
		ord := result.Data.Order
		status = strings.ToUpper(ord.Status)
		gateway = ord.Gateway
		price = fmt.Sprintf("%.2f", ord.TotalDisplay)
		currency = ord.Currency
	}
	return
}

func handleSellixStatus(rawURL, invoiceID, status, gateway, price, currency, now string) {
	priceStr := fmt.Sprintf("%s %s", price, currency)
	switch status {
	case "COMPLETED":
		bufMu.Lock()
		validBuffer = append(validBuffer, CheckoutInfo{
			URL: rawURL, Status: StatusValid,
			Method: gateway, Price: priceStr,
			InvoiceID: invoiceID, OrderStatus: status, LastChecked: now,
		})
		bufMu.Unlock()
		fmt.Printf("[VALID]   %s | %s | %s | %s\n", rawURL, status, gateway, priceStr)
	case "VOIDED", "CANCELLED", "EXPIRED":
		markInvalid(rawURL, fmt.Sprintf("Sellix status: %s", status))
	default:
		markUnknown(rawURL, fmt.Sprintf("Sellix status: %s", status))
	}
}

// ──────────────────────────────────────────
//  Helpers
// ──────────────────────────────────────────

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
//  Config y proxies
// ──────────────────────────────────────────

func loadConfig() {
	f, err := os.Open(configFile)
	if err != nil {
		fmt.Println("[INFO] config.json no encontrado, sin auth")
		return
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		fmt.Println("[WARN] Error parseando config.json:", err)
	} else {
		fmt.Println("[INFO] config.json cargado")
	}
}

func loadProxies() {
	lines, err := loadLines(proxiesFile)
	if err != nil || len(lines) == 0 {
		fmt.Println("[INFO] proxies.txt vacío, sin proxies")
		return
	}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if !strings.HasPrefix(l, "http") && !strings.HasPrefix(l, "socks") {
			l = "http://" + l
		}
		proxies = append(proxies, l)
	}
	fmt.Printf("[INFO] %d proxies cargados\n", len(proxies))
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
