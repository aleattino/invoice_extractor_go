package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
)

const (
	defaultPort        = "8080"
	mistralModel       = "mistral-small-latest"
	mistralAPIURL      = "https://api.mistral.ai/v1/chat/completions"
	outputDir          = "output"
	maxUploadSize      = 20 << 20 // 20 MB
	maxMemoryMultipart = 1 << 20
	sogliaOCR          = 50
)

func getPort() string {
	if p := os.Getenv("PORT"); p != "" {
		return p
	}
	return defaultPort
}

func getCORSOrigin() string {
	if o := os.Getenv("CORS_ORIGIN"); o != "" {
		return o
	}
	return "*"
}

// un client solo per tutte le chiamate a Mistral, così non si apre una connessione nuova ogni volta
var mistralClient = &http.Client{Timeout: 60 * time.Second}

// strutture per i dati della fattura

type VoceFattura struct {
	Descrizione    string  `json:"descrizione"`
	Quantita       float64 `json:"quantita"`
	PrezzoUnitario float64 `json:"prezzo_unitario"`
	Totale         float64 `json:"totale"`
}

type Soggetto struct {
	Nome       string `json:"nome"`
	PartitaIVA string `json:"partita_iva"`
	Indirizzo  string `json:"indirizzo"`
}

// strutture per parlare con Mistral	
// il campo ResponseFormat è importante: senza, Mistral risponde in testo libero

type MistralMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type MistralResponseFormat struct {
	Type string `json:"type"`
}

type MistralRequest struct {
	Model          string                `json:"model"`
	Messages       []MistralMessage      `json:"messages"`
	ResponseFormat MistralResponseFormat `json:"response_format"`
	Temperature    float64               `json:"temperature"`
}

type MistralChoice struct {
	Message MistralMessage `json:"message"`
}

type MistralResponse struct {
	Choices []MistralChoice `json:"choices"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// estrazione testo dal PDF

// questa funzione decide come estrarre il testo:
// prima prova il modo veloce (testo già dentro il PDF),
// se viene fuori poco o niente vuol dire che è una scansione/foto
// e allora passa a OCR che è più lento ma legge le immagini.
func estraiTestoPDF(percorso string) (string, error) {
	testo, err := estraiTestoNativo(percorso)
	if err != nil {
		log.Printf("[INFO] Estrazione nativa fallita (%v), provo OCR...", err)
	}

	if len(strings.TrimSpace(testo)) >= sogliaOCR {
		return testo, nil
	}

	log.Printf("[INFO] Testo nativo insufficiente (%d caratteri), avvio OCR...", len(strings.TrimSpace(testo)))
	testoOCR, err := estraiTestoOCR(percorso)
	if err != nil {
		return "", fmt.Errorf("sia l'estrazione nativa che OCR sono fallite: %w", err)
	}

	if strings.TrimSpace(testoOCR) == "" {
		return "", fmt.Errorf("nessun testo estratto dal PDF (né nativo né OCR)")
	}

	return testoOCR, nil
}

// modo veloce: legge il testo che è già dentro il PDF.
// funziona con i PDF "normali" (quelli dove puoi selezionare il testo).
// con le scansioni non tira fuori niente, per quello c'è estraiTestoOCR
func estraiTestoNativo(percorso string) (string, error) {
	f, reader, err := pdf.Open(percorso)
	if err != nil {
		return "", fmt.Errorf("apertura PDF fallita: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	numPagine := reader.NumPage()

	for i := 1; i <= numPagine; i++ {
		pagina := reader.Page(i)
		if pagina.V.IsNull() {
			continue
		}
		testo, err := pagina.GetPlainText(nil)
		if err != nil {
			log.Printf("[WARN] Impossibile estrarre testo dalla pagina %d: %v", i, err)
			continue
		}
		buf.WriteString(testo)
		buf.WriteString("\n")
	}

	return strings.TrimSpace(buf.String()), nil
}

// modo lento: per le scansioni e le foto.
// converte ogni pagina in un'immagine PNG (con pdftoppm) e poi
// ci passa sopra tesseract per leggere il testo.
// servono installati poppler tesseract tesseract-lang
// (tesseract-lang serve per la lingua italiana)
//
// nota: ho usato pdftoppm invece di ImageMagick perché ImageMagick
// ha dei problemi di policy con i PDF e bisogna smanettare con i config
func estraiTestoOCR(percorso string) (string, error) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return "", fmt.Errorf("pdftoppm non trovato, installa poppler (brew install poppler)")
	}
	if _, err := exec.LookPath("tesseract"); err != nil {
		return "", fmt.Errorf("tesseract non trovato, installa tesseract (brew install tesseract)")
	}

	tmpDir, err := os.MkdirTemp("", "fattura-ocr-*")
	if err != nil {
		return "", fmt.Errorf("creazione directory temporanea fallita: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	prefisso := filepath.Join(tmpDir, "page")
	cmd := exec.Command("pdftoppm", "-r", "300", "-png", percorso, prefisso) // 300 DPI, sotto viene una schifezza
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("pdftoppm fallito: %w, output: %s", err, string(output))
	}

	immagini, err := filepath.Glob(filepath.Join(tmpDir, "page-*.png"))
	if err != nil || len(immagini) == 0 {
		return "", fmt.Errorf("nessuna immagine generata da pdftoppm")
	}
	sort.Strings(immagini)

	var buf bytes.Buffer
	for _, img := range immagini {
		// prova prima in italiano
		// se non ha il pacchetto lingua ita riprova in inglese
		cmd := exec.Command("tesseract", img, "stdout", "-l", "ita")
		output, err := cmd.Output()
		if err != nil {
			log.Printf("[WARN] Tesseract fallito su %s con lingua ita, provo eng...", filepath.Base(img))
			cmd = exec.Command("tesseract", img, "stdout", "-l", "eng")
			output, err = cmd.Output()
			if err != nil {
				log.Printf("[WARN] Tesseract fallito anche con eng su %s: %v", filepath.Base(img), err)
				continue
			}
		}
		buf.Write(output)
		buf.WriteString("\n")
	}

	return strings.TrimSpace(buf.String()), nil
}

// chiamata a Mistral 
// lo schema JSON lo metto direttamente nel prompt così sa esattamente
// cosa deve restituire
func costruisciPrompt() string {
	return `Sei un assistente specializzato nell'estrazione di dati da fatture italiane.
Ti verrà fornito il testo estratto da un PDF di una fattura.

Estrai i dati e restituisci SOLO un oggetto JSON valido con questa struttura esatta:
{
  "numero_fattura": "",
  "data_emissione": "",
  "fornitore": {
    "nome": "",
    "partita_iva": "",
    "indirizzo": ""
  },
  "cliente": {
    "nome": "",
    "partita_iva": "",
    "indirizzo": ""
  },
  "importo_netto": 0,
  "iva": 0,
  "importo_totale": 0,
  "voci": [
    {
      "descrizione": "",
      "quantita": 0,
      "prezzo_unitario": 0,
      "totale": 0
    }
  ]
}

Regole:
- I campi numerici devono essere numeri, non stringhe
- Se un campo non è presente nella fattura, lascia stringa vuota o 0
- Le date vanno nel formato GG/MM/AAAA
- Non aggiungere campi extra, rispetta esattamente lo schema
- Restituisci SOLO il JSON, nessun testo aggiuntivo`
}

// manda il testo della fattura a Mistral e si aspetta indietro un JSON
// con tutti i campi compilati.
// temperature a 0 = niente creatività, voglio che copi i dati e basta.
// il ctx serve perché se l'utente chiude la pagina non ha senso
// continuare a chiamare Mistral e spendere crediti
func chiamaMistral(ctx context.Context, testoPDF string) (json.RawMessage, error) {
	apiKey := os.Getenv("MISTRAL_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("variabile d'ambiente MISTRAL_API_KEY non impostata")
	}

	reqBody := MistralRequest{
		Model: mistralModel,
		Messages: []MistralMessage{
			{Role: "system", Content: costruisciPrompt()},
			{Role: "user", Content: "Ecco il testo della fattura da analizzare:\n\n" + testoPDF},
		},
		ResponseFormat: MistralResponseFormat{Type: "json_object"},
		Temperature:    0,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("serializzazione request Mistral fallita: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", mistralAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("creazione HTTP request fallita: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := mistralClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chiamata API Mistral fallita: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("lettura risposta Mistral fallita: %w", err)
	}

	// i dettagli dell'errore li loggo solo nel terminale,
	// all'utente manda un messaggio generico
	if resp.StatusCode != http.StatusOK {
		log.Printf("[ERRORE] Mistral API status %d, body: %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("Mistral API ha risposto con errore (status %d)", resp.StatusCode)
	}

	var mistralResp MistralResponse
	if err := json.Unmarshal(respBody, &mistralResp); err != nil {
		return nil, fmt.Errorf("parsing risposta Mistral fallita: %w", err)
	}

	if len(mistralResp.Choices) == 0 {
		return nil, fmt.Errorf("Mistral non ha restituito alcuna risposta")
	}

	// Mistral restituisce il JSON come stringa dentro il campo Content.
	// lo devo ri-parsare per due motivi: controllare che sia JSON valido
	// e mandarlo al frontend senza che diventi JSON-dentro-JSON
	contenuto := mistralResp.Choices[0].Message.Content
	var jsonValidato json.RawMessage
	if err := json.Unmarshal([]byte(contenuto), &jsonValidato); err != nil {
		log.Printf("[ERRORE] JSON non valido da Mistral: %s", contenuto)
		return nil, fmt.Errorf("la risposta di Mistral non è JSON valido")
	}

	return jsonValidato, nil
}

// salvataggio e utilità

func salvaOutput(datiJSON json.RawMessage) (string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("creazione directory output fallita: %w", err)
	}

	var formattato bytes.Buffer
	if err := json.Indent(&formattato, datiJSON, "", "  "); err != nil {
		return "", fmt.Errorf("formattazione JSON fallita: %w", err)
	}

	timestamp := time.Now().Format("2006-01-02_15-04-05")
	nomeFile := fmt.Sprintf("fattura_%s.json", timestamp)
	percorso := filepath.Join(outputDir, nomeFile)

	if err := os.WriteFile(percorso, formattato.Bytes(), 0644); err != nil {
		return "", fmt.Errorf("scrittura file output fallita: %w", err)
	}

	return percorso, nil
}

func rispondiErrore(w http.ResponseWriter, messaggio string, statusCode int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(ErrorResponse{Error: messaggio}); err != nil {
		log.Printf("[ERRORE] Impossibile scrivere risposta di errore al client: %v", err)
	}
}

// HTTP

// middleware che aggiunge gli header CORS a tutte le risposte.
// senza questo, se il frontend sta su un dominio diverso dal backend
// il browser blocca le richieste
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", getCORSOrigin())
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// qui arriva il PDF, estrae il testo, lo manda a Mistral,
// salva il JSON e risponde al frontend
func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		rispondiErrore(w, "Metodo non consentito, usa POST", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	if err := r.ParseMultipartForm(maxMemoryMultipart); err != nil {
		log.Printf("[ERRORE] Parsing multipart fallito: %v", err)
		rispondiErrore(w, "File troppo grande o formato non valido (max 20MB)", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("pdf")
	if err != nil {
		log.Printf("[ERRORE] Recupero file dal form fallito: %v", err)
		rispondiErrore(w, "Nessun file PDF ricevuto", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".pdf") {
		rispondiErrore(w, "Il file deve essere un PDF", http.StatusBadRequest)
		return
	}

	// devo salvare il PDF su disco perché la libreria che lo legge
	// vuole un file, non accetta uno stream
	tmpFile, err := os.CreateTemp("", "fattura-*.pdf")
	if err != nil {
		log.Printf("[ERRORE] Creazione file temporaneo fallita: %v", err)
		rispondiErrore(w, "Errore interno del server", http.StatusInternalServerError)
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmpFile, file); err != nil {
		tmpFile.Close()
		log.Printf("[ERRORE] Copia file temporaneo fallita: %v", err)
		rispondiErrore(w, "Errore nella lettura del file", http.StatusInternalServerError)
		return
	}
	tmpFile.Close()

	log.Printf("[INFO] Estrazione testo da: %s", header.Filename)
	testoPDF, err := estraiTestoPDF(tmpPath)
	if err != nil {
		log.Printf("[ERRORE] Estrazione testo fallita per %s: %v", header.Filename, err)
		rispondiErrore(w, "Impossibile estrarre testo dal PDF: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	log.Printf("[INFO] Testo estratto (%d caratteri), invio a Mistral...", len(testoPDF))
	log.Printf("[DEBUG] Prime 200 caratteri del testo: %.200s", testoPDF)

	datiJSON, err := chiamaMistral(r.Context(), testoPDF)
	if err != nil {
		log.Printf("[ERRORE] Chiamata Mistral fallita per %s: %v", header.Filename, err)
		rispondiErrore(w, "Errore nell'analisi della fattura: "+err.Error(), http.StatusBadGateway)
		return
	}

	percorsoFile, err := salvaOutput(datiJSON)
	if err != nil {
		log.Printf("[ERRORE] Salvataggio output fallito: %v", err)
		rispondiErrore(w, "Errore nel salvataggio del risultato: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[INFO] Risultato salvato in: %s", percorsoFile)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	risposta := map[string]interface{}{
		"dati": datiJSON,
		"file": percorsoFile,
	}
	if err := json.NewEncoder(w).Encode(risposta); err != nil {
		log.Printf("[ERRORE] Impossibile scrivere risposta al client: %v", err)
	}
}

func main() {
	if os.Getenv("MISTRAL_API_KEY") == "" {
		log.Println("[WARN] MISTRAL_API_KEY non impostata, le chiamate API falliranno")
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir("static")))
	mux.HandleFunc("/upload", handleUpload)

	port := getPort()
	server := &http.Server{
		Addr:         ":" + port,
		Handler:      corsMiddleware(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("Server avviato su http://localhost:%s", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Avvio server fallito: %v", err)
	}
}
