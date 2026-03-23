FROM golang:1.21-bullseye

# Instalar Chrome (necesario para chromedp)
RUN apt-get update && apt-get install -y \
    chromium \
    --no-install-recommends && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copiar módulos primero (caché de capas)
COPY go.mod ./

# Generar go.sum y descargar dependencias
RUN go mod tidy && go mod download

# Copiar el resto
COPY . .

# Compilar
RUN go build -ldflags="-w -s" -o out main.go

CMD ["./out"]
