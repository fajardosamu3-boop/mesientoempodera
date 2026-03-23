package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	mu sync.Mutex

	validSet sync.Map
	deadSet  sync.Map
)

// ---------------- CONFIG ----------------
const (
	inputFile  = "targets.txt"
	validFile  = "checkout.txt"
	deadFile   = "dead.txt"
	checkDelay = 30 * time.Second
)

// ---------------- MAIN ----------------
func main() {
	fmt.Println("[START] Monitor de checkouts")

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

		fmt.Println("[WAIT] siguiente ciclo...")
		time.Sleep(checkDelay)
	}
}

// ---------------- CHECK ----------------
func check(link string) {

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(link)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := strings.ToLower(string(bodyBytes))

	// 🔥 detección real de pagos
	paymentSignals := []string{
		"stripe",
		"paypal",
		"card",
		"credit card",
		"visa",
		"mastercard",
		"crypto",
		"bitcoin",
		"ethereum",
		"usdt",
		"wallet",
		"payment",
		"pago",
		"checkout",
	}

	matches := 0
	for _, s := range paymentSignals {
		if strings.Contains(body, s) {
			matches++
		}
	}

	if resp.StatusCode == 200 && matches >= 2 {
		if _, ok := validSet.LoadOrStore(link, true); !ok {
			save(validFile, link)
			fmt.Println("[VALID]", link)
		}
		return
	}

	// marcar como muerto
	if _, ok := deadSet.LoadOrStore(link, true); !ok {
		save(deadFile, link)
		fmt.Println("[DEAD]", link)
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

func save(file, text string) {
	mu.Lock()
	defer mu.Unlock()

	f, _ := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	defer f.Close()
	f.WriteString(text + "\n")
}
