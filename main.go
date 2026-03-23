package main

import (
	"bufio"
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

const baseURL = "https://resellme.xyz/checkout/"

var (
	validCount   int
	invalidCount int
	retryCount   int

	mu sync.Mutex

	// anti-duplicados en memoria
	seenLinks sync.Map
	savedSet  sync.Map

	// pool dinámico de id1
	id1Pool = []string{
		"cd19684beb903",
		"77b6e436f9eab",
		"16efd1b037478",
	}
)

// ---------------- MAIN ----------------
func main() {
	rand.Seed(time.Now().UnixNano())

	proxies, _ := loadLines("proxies.txt")

	jobs := make(chan string, 2000)

	// 🔥 workers balanceados
	for i := 0; i < 25; i++ {
		go worker(jobs, proxies)
	}

	go generator(jobs)
	go stats()

	select {}
}

// ---------------- GENERATOR ----------------
func generator(jobs chan<- string) {
	for {
		id1 := getRandomID1()
		id2 := fmt.Sprintf("%013d", rand.Int63n(2000000000000))

		link := baseURL + id1 + "-" + id2

		// evitar duplicados
		if _, exists := seenLinks.Load(link); exists {
			continue
		}
		seenLinks.Store(link, true)

		jobs <- link
	}
}

// ---------------- WORKER ----------------
func worker(jobs chan string, proxies []string) {
	for link := range jobs {

		proxy := getProxy(proxies)
		status := check(link, proxy)

		switch status {
		case "VALID":
			saveUnique("valid.txt", link)
			saveUnique("checkout.txt", link) // 🔥 NUEVO
			extractID1(link)

		case "RETRY":
			time.Sleep(2 * time.Second)
			jobs <- link
		}

		// delay anti-ban
		time.Sleep(time.Duration(rand.Intn(200)+100) * time.Millisecond)
	}
}

// ---------------- CHECK ----------------
func check(link string, proxy string) string {

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	if proxy != "" {
		p, err := url.Parse(proxy)
		if err == nil {
			client.Transport = &http.Transport{
				Proxy: http.ProxyURL(p),
			}
		}
	}

	req, _ := http.NewRequest("GET", link, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil {
		log("RETRY", link)
		return "RETRY"
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		log("RETRY", link)
		return "RETRY"
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := strings.ToLower(string(bodyBytes))

	if resp.StatusCode == 200 {
		if strings.Contains(body, "checkout") || strings.Contains(body, "pagar") {
			log("VALID", link)
			return "VALID"
		}
		log("INVALID", link)
		return "INVALID"
	}

	log("INVALID", link)
	return "INVALID"
}

// ---------------- HELPERS ----------------
func getProxy(proxies []string) string {
	if len(proxies) == 0 {
		return ""
	}
	return proxies[rand.Intn(len(proxies))]
}

func getRandomID1() string {
	mu.Lock()
	defer mu.Unlock()
	return id1Pool[rand.Intn(len(id1Pool))]
}

// 🔥 aprende nuevos id1 automáticamente
func extractID1(link string) {
	parts := strings.Split(link, "/checkout/")
	if len(parts) < 2 {
		return
	}
	idPart := parts[1]
	id1 := strings.Split(idPart, "-")[0]

	mu.Lock()
	defer mu.Unlock()

	for _, v := range id1Pool {
		if v == id1 {
			return
		}
	}

	id1Pool = append(id1Pool, id1)
	saveUnique("id1.txt", id1)
}

// ---------------- FILE ----------------
func saveUnique(file, text string) {
	key := file + "|" + text

	if _, loaded := savedSet.LoadOrStore(key, true); loaded {
		return
	}

	f, _ := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	defer f.Close()
	f.WriteString(text + "\n")
}

func loadLines(file string) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, strings.TrimSpace(scanner.Text()))
	}
	return lines, nil
}

// ---------------- LOG ----------------
func log(status, link string) {
	mu.Lock()
	defer mu.Unlock()

	switch status {
	case "VALID":
		validCount++
		fmt.Println("[VALID]", link)
	case "INVALID":
		invalidCount++
	case "RETRY":
		retryCount++
	}
}

// ---------------- STATS ----------------
func stats() {
	for {
		mu.Lock()
		fmt.Printf("[STATS] V:%d I:%d R:%d | ID1:%d\n",
			validCount, invalidCount, retryCount, len(id1Pool))
		mu.Unlock()

		time.Sleep(5 * time.Second)
	}
}
