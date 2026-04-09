// kakitu_wallet_server/stripe/cardholder.go
package stripeissuing

import (
	"os"

	"github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/issuing/card"
	"github.com/stripe/stripe-go/v76/issuing/cardholder"
)

func init() {
	stripe.Key = os.Getenv("STRIPE_SECRET_KEY")
}

type CardholderParams struct {
	Name       string
	Phone      string
	Line1      string
	City       string
	Country    string
	PostalCode string
}

type IssuedCard struct {
	CardholderID string
	CardID       string
	Last4        string
	ExpMonth     int64
	ExpYear      int64
}

type CardDetails struct {
	Number   string
	CVC      string
	ExpMonth int64
	ExpYear  int64
}

// CreateCardholder creates a Stripe Issuing cardholder and immediately issues a virtual card.
func CreateCardholder(p CardholderParams) (*IssuedCard, error) {
	ch, err := cardholder.New(&stripe.IssuingCardholderParams{
		Params: stripe.Params{
			IdempotencyKey: stripe.String("ch-" + p.Phone),
		},
		Name:        stripe.String(p.Name),
		Type:        stripe.String("individual"),
		Status:      stripe.String("active"),
		PhoneNumber: stripe.String(p.Phone),
		Billing: &stripe.IssuingCardholderBillingParams{
			Address: &stripe.AddressParams{
				Line1:      stripe.String(p.Line1),
				City:       stripe.String(p.City),
				Country:    stripe.String(p.Country),
				PostalCode: stripe.String(p.PostalCode),
			},
		},
	})
	if err != nil {
		return nil, err
	}

	c, err := card.New(&stripe.IssuingCardParams{
		Params: stripe.Params{
			IdempotencyKey: stripe.String("card-" + p.Phone),
		},
		Cardholder: stripe.String(ch.ID),
		Currency:   stripe.String("eur"),
		Type:       stripe.String("virtual"),
	})
	if err != nil {
		return nil, err
	}

	// Activate the card
	_, err = card.Update(c.ID, &stripe.IssuingCardParams{
		Status: stripe.String(string(stripe.IssuingCardStatusActive)),
	})
	if err != nil {
		return nil, err
	}

	return &IssuedCard{
		CardholderID: ch.ID,
		CardID:       c.ID,
		Last4:        c.Last4,
		ExpMonth:     c.ExpMonth,
		ExpYear:      c.ExpYear,
	}, nil
}

// GetCardDetails fetches the sensitive card data (number, CVC) from Stripe.
// Never cache the result — call fresh each time the user requests to see details.
func GetCardDetails(cardID string) (*CardDetails, error) {
	c, err := card.Get(cardID, &stripe.IssuingCardParams{
		Params: stripe.Params{
			Expand: []*string{
				stripe.String("number"),
				stripe.String("cvc"),
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return &CardDetails{
		Number:   c.Number,
		CVC:      c.CVC,
		ExpMonth: c.ExpMonth,
		ExpYear:  c.ExpYear,
	}, nil
}
