# syntax=docker/dockerfile:1

# ============================================================
# STAGE 1: builder
# Usamos a imagem oficial do Go para compilar.
# Multi-stage build: a imagem final não vai ter o compilador Go,
# apenas o binário — imagem menor e mais segura.
# ============================================================
FROM golang:1.22-alpine AS builder

# Instala dependências de build mínimas.
# ca-certificates: necessário para TLS em binários Go estáticos.
RUN apk add --no-cache ca-certificates git

WORKDIR /workspace

# Copia go.mod e go.sum primeiro para aproveitar o cache do Docker.
# Se só o código mudar (não as deps), essa camada é reutilizada.
COPY go.mod go.sum ./
RUN go mod download

# Copia o restante do código.
COPY . .

# Compila o binário.
# CGO_ENABLED=0: binário estático — não depende de libc do sistema.
# GOOS=linux: garante que o binário é para Linux mesmo compilando em Mac/Windows.
# -ldflags="-w -s": remove debug info e symbol table → binário menor.
# -trimpath: remove paths absolutos do binário → builds reproduzíveis.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s" \
    -trimpath \
    -o manager \
    ./cmd/main.go

# ============================================================
# STAGE 2: runtime
# Usamos distroless: imagem mínima sem shell, sem package manager.
# Superfície de ataque mínima — padrão para operators em produção.
# ============================================================
FROM gcr.io/distroless/static:nonroot

WORKDIR /

# Copia apenas o binário compilado do stage anterior.
COPY --from=builder /workspace/manager .

# nonroot user (UID 65532) — não rodar como root é obrigatório em
# clusters com PodSecurityAdmission (padrão no Kubernetes 1.25+).
USER 65532:65532

# O manager expõe:
# - 9443: webhook server (TLS)
# - 8080: metrics (Prometheus)
# - 8081: health probes
EXPOSE 9443 8080 8081

ENTRYPOINT ["/manager"]
