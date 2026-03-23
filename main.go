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

	"github.com/chromedp/chromedp"
)

type CheckoutInfo struct {
	URL          string `json:"url"`
	Status       string `json:"status"`        // VALID / UNKNOWN
	Method       string `json:"payment_method"` // Stripe / PayPal / Crypto / Unknown
	Price        string `json:"price,omitempty"`
	LastChecked  string `json:"last_checked"`
}

var (
	mu sync.Mutex

	validSet   sync.Map
	unknownSet sync.Map

	bufferMu      sync.Mutex
	validBuffer   []CheckoutInfo
	unknownBuffer []string
)

const (
	inputFile     = "targets.txt"
	jsonFile      = "checkout.json"
	unknownFile   = "unknown.txt"
	checkDelay    = 30 * time.Second
	headlessWait  = 2 * time.Second
)

func main() {
	fmt.Println("[START] Ultra PRO++ Monitor de checkouts con extracción de datos")

	for {
		targets, err := loadLines(inputFile)
		if err != nil {
			fmt.Println("Error leyendo targets:", err)
			time.Sleep(10 * time.Second)
			continue
		}

		var wg sync.WaitGroup
		for _, url := range targets {
			wg.Add(1)
			go func(u string) {
				defer wg.Done()
				check(u)
			}(url)
		}

		wg.Wait()
		flushBuffers()
		fmt.Println("[WAIT] siguiente ciclo...")
		time.Sleep(checkDelay)
	}
}

// ---------------- CHECK ----------------
func check(link string) {
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var htmlContent string
	err := chromedp.Run(ctx,
		chromedp.Navigate(link),
		chromedp.Sleep(headlessWait),
		chromedp.OuterHTML("html", &htmlContent, chromedp.NodeVisible),
	)
	if err != nil {
		fmt.Println("[DEAD]", link)
		return
	}

	body := strings.ToLower(htmlContent)
	now := time.Now().Format(time.RFC3339)

	method := "Unknown"

	// Detecta scripts de pago
	if strings.Contains(body, "js.stripe.com") || strings.Contains(body, "checkout-button") {
		method = "Stripe"
	}
	if strings.Contains(body, "paypal.com/sdk/js") {
		method = "PayPal"
	}
	if strings.Contains(body, "crypto") || strings.Contains(body, "bitcoin") || strings.Contains(body, "ethereum") {
		method = "Crypto"
	}

	// Extrae precio aproximado si aparece en HTML
	price := ""
	priceIndicators := []string{"$", "€", "usd", "eur"}
	for _, p := range priceIndicators {
		if idx := strings.Index(body, p); idx != -1 && idx+10 < len(body) {
			price = body[idx : idx+10]
			price = strings.Fields(price)[0] // tomar primera palabra
			break
		}
	}

	info := CheckoutInfo{
		URL:         link,
		LastChecked: now,
		Method:      method,
		Price:       price,
	}

	// -------- VALID si detecta método de pago --------
	if method != "Unknown" {
		info.Status = "VALID"
		if _, ok := validSet.LoadOrStore(link, true); !ok {
			addCheckoutBuffer(info)
			fmt.Println("[VALID]", link, "| Method:", method, "| Price:", price)
		}
		return
	}

	// -------- UNKNOWN --------
	info.Status = "UNKNOWN"
	if _, ok := unknownSet.LoadOrStore(link, true); !ok {
		addUnknownBuffer(link)
		fmt.Println("[UNKNOWN]", link)
	}
}

// ---------------- HELPERS ----------------
func addCheckoutBuffer(info CheckoutInfo) {
	bufferMu.Lock()
	defer bufferMu.Unlock()
	validBuffer = append(validBuffer, info)
}

func addUnknownBuffer(link string) {
	bufferMu.Lock()
	defer bufferMu.Unlock()
	unknownBuffer = append(unknownBuffer, link)
}

// flush buffers a archivos
func flushBuffers() {
	bufferMu.Lock()
	defer bufferMu.Unlock()

	if len(validBuffer) > 0 {
		saveJSON(jsonFile, validBuffer)
		validBuffer = nil
	}

	if len(unknownBuffer) > 0 {
		saveLines(unknownFile, unknownBuffer)
		unknownBuffer = nil
	}
}

// ---------------- FILE ----------------
func loadLines(file string) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func saveLines(file string, lines []string) {
	mu.Lock()
	defer mu.Unlock()

	f, _ := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	defer f.Close()
	for _, line := range lines {
		f.WriteString(line + "\n")
	}
}

func saveJSON(file string, data []CheckoutInfo) {
	mu.Lock()
	defer mu.Unlock()

	var all []CheckoutInfo
	// leer si ya existe
	f, err := os.Open(file)
	if err == nil {
		decoder := json.NewDecoder(f)
		decoder.Decode(&all)
		f.Close()
	}

	// append nuevos
	all = append(all, data...)

	// escribir todo
	out, _ := os.Create(file)
	defer out.Close()
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	encoder.Encode(all)
}
