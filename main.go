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
	LastChecked string `json:"last_checked"`
}

type Config struct {
	APIKey      string `json:"api_key"`
	BearerToken string `json:"bearer_token"`
}

// Respuesta de la API de resellme
type ResellmeInvoice struct {
	Status string `json:"status"` // "completed", "pending", "expired", etc.
	Error  string `json:"error,omitempty"`
	// Añade más campos si la API devuelve más datos útiles
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

// Señales de página muerta en HTML (fallback si la API falla)
var invalidSignals = []string{
	"factura no encontrada",
	"invoice not found",
	"order not found",
	"404",
	"not found",
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
//  HTTP client (con proxy si hay)
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
//  Ejemplo: https://resellme.xyz/checkout/6201d27b63af0-0000010143560
//           → 6201d27b63af0-0000010143560
// ──────────────────────────────────────────

func extractInvoiceID(rawURL string) string {
	parts := strings.Split(strings.TrimRight(rawURL, "/"), "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
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

	// ── 1. Intentar API de resellme ───────────────────────────
	// Probamos los endpoints más comunes de APIs de tiendas similares
	apiEndpoints := []string{
		fmt.Sprintf("https://resellme.xyz/api/invoice/%s", invoiceID),
		fmt.Sprintf("https://resellme.xyz/api/checkout/%s", invoiceID),
		fmt.Sprintf("https://resellme.xyz/api/order/%s", invoiceID),
	}

	client := newClient()
	now := time.Now().Format(time.RFC3339)

	for _, apiURL := range apiEndpoints {
		req, err := http.NewRequest("GET", apiURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Referer", rawURL)

		if cfg.BearerToken != "" && cfg.BearerToken != "AQUI_PON_TU_TOKEN_SI_ES_BEARER" {
			req.Header.Set("Authorization", "Bearer "+cfg.BearerToken)
		} else if cfg.APIKey != "" && cfg.APIKey != "AQUI_PON_TU_API_KEY" {
			req.Header.Set("X-API-Key", cfg.APIKey)
		}

		resp, err := client.Do(req)
		if err != nil || resp.StatusCode == 404 {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}
		defer resp.Body.Close()

		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		body := string(bodyBytes)
		bodyLower := strings.ToLower(body)

		// Intentar parsear JSON
		var invoice ResellmeInvoice
		if err := json.Unmarshal(bodyBytes, &invoice); err == nil {
			switch strings.ToLower(invoice.Status) {
			case "completed", "paid", "complete", "delivered":
				info := CheckoutInfo{
					URL:         rawURL,
					Status:      StatusValid,
					Method:      "Resellme",
					Price:       "",
					LastChecked: now,
				}
				bufMu.Lock()
				validBuffer = append(validBuffer, info)
				bufMu.Unlock()
				fmt.Printf("[VALID]   %s | estado API: %s\n", rawURL, invoice.Status)
				return
			case "not found", "error", "invalid", "expired":
				markInvalid(rawURL, fmt.Sprintf("estado API: %s", invoice.Status))
				return
			default:
				markUnknown(rawURL, fmt.Sprintf("estado API desconocido: %s", invoice.Status))
				return
			}
		}

		// Si no es JSON válido pero responde, buscar señales en el texto
		if resp.StatusCode == 200 {
			for _, sig := range invalidSignals {
				if strings.Contains(bodyLower, sig) {
					markInvalid(rawURL, fmt.Sprintf("señal en API: %q", sig))
					return
				}
			}
			// Responde 200 sin señales negativas → probablemente válido
			if strings.Contains(bodyLower, "completed") || strings.Contains(bodyLower, "entregado") || strings.Contains(bodyLower, "paid") {
				info := CheckoutInfo{
					URL:         rawURL,
					Status:      StatusValid,
					Method:      "Resellme",
					LastChecked: now,
				}
				bufMu.Lock()
				validBuffer = append(validBuffer, info)
				bufMu.Unlock()
				fmt.Printf("[VALID]   %s | señal positiva en body\n", rawURL)
				return
			}
		}
		_ = body
	}

	// ── 2. Fallback: GET a la página normal y buscar señales ──
	// resellme es SPA, el HTML estático no tendrá contenido dinámico,
	// pero a veces devuelve datos en meta tags o el status HTTP
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		markUnknown(rawURL, "URL inválida")
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("[DEAD]   %s | %v\n", rawURL, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 || resp.StatusCode == 410 {
		markInvalid(rawURL, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return
	}

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	bodyLower := strings.ToLower(string(bodyBytes))

	for _, sig := range invalidSignals {
		if strings.Contains(bodyLower, sig) {
			markInvalid(rawURL, fmt.Sprintf("señal HTML: %q", sig))
			return
		}
	}

	// SPA cargó bien pero no podemos leer el estado sin JS
	markUnknown(rawURL, "SPA sin API accesible — requiere ejecución JS")
}

// ──────────────────────────────────────────
//  Helpers de estado
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
//  Extracción de precio
// ──────────────────────────────────────────

func extractPrice(body string) string {
	indicators := []string{"$", "€", "£", "usd", "eur", "gbp"}
	for _, ind := range indicators {
		idx := strings.Index(body, ind)
		if idx == -1 {
			continue
		}
		end := idx + 15
		if end > len(body) {
			end = len(body)
		}
		snippet := strings.TrimSpace(body[idx:end])
		if i := strings.IndexAny(snippet, " \n\r\t<"); i > 0 {
			snippet = snippet[:i]
		}
		if len(snippet) > 1 {
			return snippet
		}
	}
	return ""
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
//  Carga de config y proxies
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
