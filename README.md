# Estrattore fatture

Applicazione web in Go che estrae dati strutturati da fatture PDF usando le API di Mistral AI.

Supporta sia PDF con testo selezionabile che PDF scansionati/foto (via OCR con Tesseract).

## Campi estratti

- Numero fattura, data emissione
- Fornitore e cliente (nome, P.IVA, indirizzo)
- Importo netto, IVA, totale
- Elenco voci con descrizione, quantità, prezzo unitario e totale

## Requisiti

- Go 1.21+
- [Poppler](https://poppler.freedesktop.org/) (per `pdftoppm`, serve per i PDF scansionati)
- [Tesseract OCR](https://github.com/tesseract-ocr/tesseract) con il pacchetto lingua italiana
- API key di [Mistral AI](https://mistral.ai/)

### Installazione dipendenze (macOS)

```bash
brew install poppler tesseract tesseract-lang
```

### Installazione dipendenze (Debian/Ubuntu)

```bash
apt-get install poppler-utils tesseract-ocr tesseract-ocr-ita
```

## Avvio locale

```bash
export MISTRAL_API_KEY="la-tua-api-key"
go run main.go
```

Apri `http://localhost:8080` nel browser.

## Variabili d'ambiente

| Variabile | Obbligatoria | Default | Descrizione |
|-----------|:---:|---------|-------------|
| `MISTRAL_API_KEY` | sì | — | API key Mistral AI |
| `PORT` | no | `8080` | Porta del server |
| `CORS_ORIGIN` | no | `*` | Origine consentita per CORS |

## Deploy su Railway

1. Collega il repo GitHub a [Railway](https://railway.app/)
2. Railway rileva il `Dockerfile` e builda automaticamente
3. Aggiungi la variabile `MISTRAL_API_KEY` nelle impostazioni del progetto
4. Railway assegna la porta tramite la variabile `PORT` (già gestita)

## Struttura

```
main.go          backend Go
static/
  index.html     frontend
output/          JSON estratti (generati a runtime)
Dockerfile       per deploy su Railway/Docker
```
