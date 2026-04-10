package reap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

var (
	baseURL = "https://sandbox.api.caas.reap.global"
	apiKey  = ""
)

func init() {
	apiKey = os.Getenv("REAP_API_KEY")
	if prod := os.Getenv("REAP_BASE_URL"); prod != "" {
		baseURL = prod
	}
}

// CardholderParams holds data needed to create a Reap virtual card.
type CardholderParams struct {
	FirstName string
	LastName  string
	Phone     string // E.164 e.g. +254712345678
	DOB       string // YYYY-MM-DD
	IDNumber  string
	MetaID    string // unique per-user identifier (ETH address)
}

// IssuedCard is returned after successful card creation.
type IssuedCard struct {
	CardID string
	Last4  string
}

// CardInfo holds non-sensitive card details available without the iframe.
type CardInfo struct {
	Last4  string
	Status string
}

// parsePhone splits an E.164 phone into Reap's dialCode + localNumber format.
// "+254712345678" → dialCode=254, localNumber="0712345678"
func parsePhone(phone string) (int, string) {
	phone = strings.TrimPrefix(phone, "+")
	// Try 3-digit country codes first (covers most of Africa: 254=KE, 255=TZ, etc.)
	if len(phone) >= 3 {
		var dc int
		fmt.Sscanf(phone[:3], "%d", &dc)
		if dc >= 200 && dc <= 299 { // Africa range
			return dc, "0" + phone[3:]
		}
	}
	// 2-digit country codes (EU: 31=NL, 33=FR, 34=ES, 39=IT, 49=DE)
	if len(phone) >= 2 {
		var dc int
		fmt.Sscanf(phone[:2], "%d", &dc)
		return dc, phone[2:]
	}
	return 0, phone
}

type reapAddress struct {
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	Country    string `json:"country"` // 3-letter ISO alpha-3 e.g. "KEN"
	PostalCode string `json:"postalCode,omitempty"`
	City       string `json:"city"`
}

type reapKYC struct {
	FirstName          string      `json:"firstName"`
	LastName           *string     `json:"lastName,omitempty"`
	DOB                string      `json:"dob"` // YYYY-MM-DD
	ResidentialAddress reapAddress `json:"residentialAddress"`
	IDDocumentType     string      `json:"idDocumentType"`   // "NationalID"
	IDDocumentNumber   string      `json:"idDocumentNumber"` // ID number
}

type reapOTPPhone struct {
	DialCode    int    `json:"dialCode"`
	PhoneNumber string `json:"phoneNumber"`
}

type reapMeta struct {
	ID             string       `json:"id"`
	OTPPhoneNumber reapOTPPhone `json:"otpPhoneNumber"`
}

type createCardReq struct {
	CardType          string   `json:"cardType"`
	CustomerType      string   `json:"customerType"`
	KYC               reapKYC  `json:"kyc"`
	PreferredCardName string   `json:"preferredCardName"` // max 27 chars
	Meta              reapMeta `json:"meta"`
}

type createCardResp struct {
	ID string `json:"id"`
}

type getCardResp struct {
	Last4  string `json:"last4"`
	Status string `json:"status"`
}

func doRequest(method, path string, body interface{}) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, baseURL+path, r)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("x-reap-api-key", apiKey)
	req.Header.Set("Accept-Version", "v2.0")
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return data, resp.StatusCode, err
}

// CreateCard creates a Reap virtual card with embedded KYC.
// Uses a default Nairobi residential address for all Kenyan users (billing address managed by Reap).
func CreateCard(p CardholderParams) (*IssuedCard, error) {
	dialCode, localPhone := parsePhone(p.Phone)

	cardName := p.FirstName + " " + p.LastName
	if len(cardName) > 27 {
		cardName = cardName[:27]
	}
	var lastNamePtr *string
	if p.LastName != "" {
		lastNamePtr = &p.LastName
	}

	payload := createCardReq{
		CardType:          "Virtual",
		CustomerType:      "Consumer",
		PreferredCardName: cardName,
		KYC: reapKYC{
			FirstName: p.FirstName,
			LastName:  lastNamePtr,
			DOB:       p.DOB,
			ResidentialAddress: reapAddress{
				Line1:      "Moi Avenue",
				Line2:      "Nairobi CBD",
				Country:    "KEN",
				PostalCode: "00100",
				City:       "Nairobi",
			},
			IDDocumentType:   "NationalID",
			IDDocumentNumber: p.IDNumber,
		},
		Meta: reapMeta{
			ID: p.MetaID,
			OTPPhoneNumber: reapOTPPhone{
				DialCode:    dialCode,
				PhoneNumber: localPhone,
			},
		},
	}

	data, status, err := doRequest("POST", "/cards", payload)
	if err != nil {
		return nil, err
	}
	if status != 201 {
		return nil, fmt.Errorf("reap error %d: %s", status, string(data))
	}

	var resp createCardResp
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}

	// Fetch card to get last4
	info, err := GetCard(resp.ID)
	if err != nil {
		return nil, err
	}

	return &IssuedCard{
		CardID: resp.ID,
		Last4:  info.Last4,
	}, nil
}

// GetCard returns non-sensitive card info (last4, status).
// Full card details (PAN, CVV, expiry) are only available via Reap's iframe widget.
func GetCard(cardID string) (*CardInfo, error) {
	data, status, err := doRequest("GET", "/cards/"+cardID, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("reap error %d: %s", status, string(data))
	}
	var resp getCardResp
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &CardInfo{
		Last4:  resp.Last4,
		Status: resp.Status,
	}, nil
}
