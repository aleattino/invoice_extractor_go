package main

import (
	"encoding/json"
	"testing"
)

func TestContaParole(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"", 0},
		{"x 1 2 3", 0},
		{"Fattura numero 123", 2},
		{"Il totale è di euro 1500", 4},
		{"a b c d e f g h i j", 0},
		{"Spett.le Azienda SRL via Roma 10", 6},
		{"--- ... ### $$$ 000", 0},
		{"Descrizione   quantità   prezzo   totale", 4},
	}

	for _, tt := range tests {
		got := contaParole(tt.input)
		if got != tt.expected {
			t.Errorf("contaParole(%q) = %d, atteso %d", tt.input, got, tt.expected)
		}
	}
}

func TestEstraiNumero(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"page-1.png", 1},
		{"page-01.png", 1},
		{"page-001.png", 1},
		{"page-12.png", 12},
		{"page-100.png", 100},
		{"nessun-numero.png", 0},
		{"page-abc.png", 0},
		{"/tmp/fattura-ocr-123/page-03.png", 3},
	}

	for _, tt := range tests {
		got := estraiNumero(tt.input)
		if got != tt.expected {
			t.Errorf("estraiNumero(%q) = %d, atteso %d", tt.input, got, tt.expected)
		}
	}
}

func TestValidaDatiFattura_Completa(t *testing.T) {
	dati := `{
		"numero_fattura": "2024/001",
		"importo_totale": 1220.00,
		"importo_netto": 1000.00,
		"fornitore": {"nome": "Azienda SRL", "partita_iva": "12345678901", "indirizzo": "Via Roma 1"},
		"cliente": {"nome": "Cliente SPA", "partita_iva": "09876543210", "indirizzo": "Via Milano 2"},
		"voci": [{"descrizione": "Servizio", "quantita": 1, "prezzo_unitario": 1000, "totale": 1000}]
	}`

	avvisi := validaDatiFattura(json.RawMessage(dati))
	if len(avvisi) != 0 {
		t.Errorf("fattura completa non dovrebbe avere avvisi, trovati: %v", avvisi)
	}
}

func TestValidaDatiFattura_Vuota(t *testing.T) {
	dati := `{
		"numero_fattura": "",
		"importo_totale": 0,
		"importo_netto": 0,
		"fornitore": {"nome": "", "partita_iva": "", "indirizzo": ""},
		"cliente": {"nome": "", "partita_iva": "", "indirizzo": ""},
		"voci": []
	}`

	avvisi := validaDatiFattura(json.RawMessage(dati))
	if len(avvisi) != 5 {
		t.Errorf("fattura vuota dovrebbe avere 5 avvisi, trovati %d: %v", len(avvisi), avvisi)
	}
}

func TestValidaDatiFattura_ImportoZeroConResto(t *testing.T) {
	dati := `{
		"numero_fattura": "2024/001",
		"importo_totale": 0,
		"importo_netto": 0,
		"fornitore": {"nome": "Azienda SRL"},
		"cliente": {"nome": "Cliente SPA"},
		"voci": [{"descrizione": "Servizio"}]
	}`

	avvisi := validaDatiFattura(json.RawMessage(dati))
	found := false
	for _, a := range avvisi {
		if a == "importo totale e netto sono entrambi 0" {
			found = true
		}
	}
	if !found {
		t.Errorf("dovrebbe segnalare importi a 0, avvisi: %v", avvisi)
	}
}

func TestValidaDatiFattura_JSONInvalido(t *testing.T) {
	avvisi := validaDatiFattura(json.RawMessage(`non è json`))
	if len(avvisi) != 1 || avvisi[0] != "impossibile validare la struttura del JSON" {
		t.Errorf("JSON invalido dovrebbe dare un avviso, trovati: %v", avvisi)
	}
}
