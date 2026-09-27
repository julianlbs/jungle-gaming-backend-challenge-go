package wagering

import "testing"

func samplePayload() CanonicalPayload {
	return CanonicalPayload{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		PlayerID:              "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		WalletID:              "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Amount:                "25.00",
		Currency:              "BRL",
	}
}

// Golden values were produced independently with Python:
// json.dumps(doc, sort_keys=True, separators=(",", ":"), ensure_ascii=False).
func TestPayloadHashGolden(t *testing.T) {
	p := samplePayload()
	if got, want := PayloadHash(p), "sha256:629836932b79106b99523d06a1e7fa80689b0ea1e1c47aa3f0a5a2c87d0c4344"; got != want {
		t.Fatalf("PayloadHash = %s, want %s", got, want)
	}

	p.Kind = "REFUND"
	p.ReferenceExternalTransactionID = "tx-<&>"
	if got, want := PayloadHash(p), "sha256:6b990fd084deeb9990d77f3493da034fc14528c9c31bbdee76766762fc61ec6d"; got != want {
		t.Fatalf("PayloadHash with reference = %s, want %s", got, want)
	}
}

func TestPayloadHashSensitiveToEveryField(t *testing.T) {
	base := PayloadHash(samplePayload())
	mutations := map[string]func(*CanonicalPayload){
		"provider":  func(p *CanonicalPayload) { p.ProviderID = "provider-b" },
		"external":  func(p *CanonicalPayload) { p.ExternalTransactionID = "transaction-124" },
		"player":    func(p *CanonicalPayload) { p.PlayerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a2" },
		"wallet":    func(p *CanonicalPayload) { p.WalletID = "0192f291-27dd-7d3f-8071-5f8685deef38" },
		"round":     func(p *CanonicalPayload) { p.RoundID = "round-988" },
		"game":      func(p *CanonicalPayload) { p.GameID = "other" },
		"kind":      func(p *CanonicalPayload) { p.Kind = "WIN" },
		"amount":    func(p *CanonicalPayload) { p.Amount = "25.01" },
		"currency":  func(p *CanonicalPayload) { p.Currency = "USD" },
		"reference": func(p *CanonicalPayload) { p.ReferenceExternalTransactionID = "x" },
	}
	for name, mutate := range mutations {
		p := samplePayload()
		mutate(&p)
		if PayloadHash(p) == base {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
	if PayloadHash(samplePayload()) != base {
		t.Error("hash must be deterministic")
	}
}
