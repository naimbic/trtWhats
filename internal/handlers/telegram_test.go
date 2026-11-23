package handlers

import (
	"strings"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
)

func TestTelegramMoney(t *testing.T) {
	cases := map[float64]string{
		0:       "0",
		50:      "50",
		999:     "999",
		1000:    "1 000",
		1500:    "1 500",
		1234567: "1 234 567",
		1499.6:  "1 500", // rounds to nearest dirham
	}
	for in, want := range cases {
		if got := telegramMoney(in); got != want {
			t.Errorf("telegramMoney(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestTelegramIntlPhone(t *testing.T) {
	cases := map[string]string{
		"212612345678": "+212612345678",
		"0612345678":   "+212612345678",
		"33612345678":  "+33612345678",
	}
	for in, want := range cases {
		if got := telegramIntlPhone(in); got != want {
			t.Errorf("telegramIntlPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTelegramWaDigits(t *testing.T) {
	cases := map[string]string{
		"0612345678":   "212612345678",
		"212612345678": "212612345678",
	}
	for in, want := range cases {
		if got := telegramWaDigits(in); got != want {
			t.Errorf("telegramWaDigits(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildTelegramSaleMessage(t *testing.T) {
	c := &models.Contact{
		ProfileName:        "Amine <B&B>",
		PhoneNumber:        "212612345678",
		City:               "Agadir",
		Address:            "Hay Salam, Rue 5",
		ConversionQuantity: 2,
		ConversionValue:    1500,
		WhatsAppAccount:    "Belle Tulipe",
		Channel:            "whatsapp",
		Tags:               models.JSONBArray{"تم البيع - Converti", "Source: Instagram Ad"},
	}
	msg := buildTelegramSaleMessage(c)

	// HTML-escapes user strings (no raw < or & from the name).
	if strings.Contains(msg, "Amine <B&B>") {
		t.Errorf("name not HTML-escaped: %q", msg)
	}
	if !strings.Contains(msg, "Amine &lt;B&amp;B&gt;") {
		t.Errorf("expected escaped name in: %q", msg)
	}
	// Money formatted with separator, no decimals, no currency symbol.
	if !strings.Contains(msg, "1 500 درهم") {
		t.Errorf("expected formatted amount in: %q", msg)
	}
	// wa.me link uses international digits.
	if !strings.Contains(msg, "https://wa.me/212612345678") {
		t.Errorf("expected wa.me link in: %q", msg)
	}
	// Phone shown as a continuous +E.164 number (no space-grouping that reverses in RTL).
	if !strings.Contains(msg, "+212612345678") {
		t.Errorf("expected intl phone in: %q", msg)
	}
	if strings.Contains(msg, "06 12 34 56 78") {
		t.Errorf("phone should not be space-grouped (reverses in RTL): %q", msg)
	}
	// Quantity + city + address present.
	for _, want := range []string{"الكمية: 2", "Agadir", "Hay Salam"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected %q in: %q", want, msg)
		}
	}
}

func TestBuildTelegramSaleMessage_MissingFields(t *testing.T) {
	c := &models.Contact{ConversionValue: 0} // nothing set
	msg := buildTelegramSaleMessage(c)
	if !strings.Contains(msg, "بيعة جديدة") {
		t.Errorf("expected header even with empty contact: %q", msg)
	}
	// No phone line when phone is blank.
	if strings.Contains(msg, "wa.me") {
		t.Errorf("did not expect wa.me link with empty phone: %q", msg)
	}
}
