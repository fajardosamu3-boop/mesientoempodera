FROM golang:1.21-bullseye

# Instalar Chrome (necesario para chromedp)
RUN apt-get update && apt-get install -y \
    chromium \
    --no-install-recommends && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copiar módulos
COPY go.mod ./

# Generar go.sum y descargar dependencias
RUN go mod tidy && go mod download

# Copiar el resto del código
COPY . .

# Compilar
RUN go build -ldflags="-w -s" -o out .

CMD ["./out"]
