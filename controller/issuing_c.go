package controller

import (
	"errors"
	"net/http"

	"github.com/go-chi/render"
	stripeissuing "github.com/kakitucurrency/kakitu-wallet-server/stripe"
	"github.com/kakitucurrency/kakitu-wallet-server/repository"
	"github.com/kakitucurrency/kakitu-wallet-server/utils"
	"k8s.io/klog/v2"
)

type IssuingController struct {
	AddressRepo *repository.StripeAddressRepo
	CardRepo    *repository.StripeCardRepo
}

// issueCardRequest is the request body for POST /issuing/card.
type issueCardRequest struct {
	Account string `json:"account"`
	Name    string `json:"name"`
	Phone   string `json:"phone"`
}

// issueCardResponse is returned on successful card issuance.
type issueCardResponse struct {
	CardID   string `json:"card_id"`
	Last4    string `json:"last4,omitempty"`
	ExpMonth int64  `json:"exp_month,omitempty"`
	ExpYear  int64  `json:"exp_year,omitempty"`
	Status   string `json:"status"`
}

// cardDetailsResponse is returned for GET /issuing/card/details.
type cardDetailsResponse struct {
	Number     string `json:"number"`
	CVC        string `json:"cvc"`
	ExpMonth   int64  `json:"exp_month"`
	ExpYear    int64  `json:"exp_year"`
	PostalCode string `json:"postal_code"`
}

// HandleIssueCard handles POST /issuing/card.
// Request body: { "account": "kshs_...", "name": "...", "phone": "..." }
func (ic *IssuingController) HandleIssueCard(w http.ResponseWriter, r *http.Request) {
	var req issueCardRequest
	if err := render.DecodeJSON(r.Body, &req); err != nil {
		ErrBadrequest(w, r, "invalid_json")
		return
	}

	// 1. Validate inputs
	if !utils.ValidateAddress(req.Account) {
		ErrBadrequest(w, r, "invalid_account")
		return
	}
	if req.Name == "" {
		ErrBadrequest(w, r, "name_required")
		return
	}
	if req.Phone == "" {
		ErrBadrequest(w, r, "phone_required")
		return
	}

	// 2. Check for existing card
	existing, err := ic.CardRepo.GetCardByAccount(req.Account)
	if err != nil {
		klog.Errorf("HandleIssueCard: GetCardByAccount error for %s: %v", req.Account, err)
		ErrInternalServerError(w, r, "db_error")
		return
	}
	if existing != nil {
		render.Status(r, http.StatusOK)
		render.JSON(w, r, &issueCardResponse{
			CardID: existing.StripeCardID,
			Status: "active",
		})
		return
	}

	// 3. Assign a pool address
	addr, err := ic.AddressRepo.AssignAddress(req.Account)
	if err != nil {
		if errors.Is(err, repository.ErrNoAddressesAvailable) {
			render.Status(r, http.StatusServiceUnavailable)
			render.JSON(w, r, &ErrorResponse{Error: "no_addresses_available"})
			return
		}
		klog.Errorf("HandleIssueCard: AssignAddress error for %s: %v", req.Account, err)
		ErrInternalServerError(w, r, "address_assignment_failed")
		return
	}

	// 4. Create cardholder and issue card via Stripe
	issued, err := stripeissuing.CreateCardholder(stripeissuing.CardholderParams{
		Name:       req.Name,
		Phone:      req.Phone,
		Line1:      addr.Line1,
		City:       addr.City,
		Country:    addr.Country,
		PostalCode: addr.PostalCode,
	})
	if err != nil {
		klog.Errorf("HandleIssueCard: CreateCardholder error for %s: %v", req.Account, err)
		ErrInternalServerError(w, r, "stripe_error")
		return
	}

	// 5. Persist card to DB
	_, err = ic.CardRepo.CreateCard(req.Account, issued.CardholderID, issued.CardID, addr.ID.String())
	if err != nil {
		klog.Errorf("HandleIssueCard: CreateCard DB error for %s: %v", req.Account, err)
		ErrInternalServerError(w, r, "db_error")
		return
	}

	// 6. Return card info
	render.Status(r, http.StatusOK)
	render.JSON(w, r, &issueCardResponse{
		CardID:   issued.CardID,
		Last4:    issued.Last4,
		ExpMonth: issued.ExpMonth,
		ExpYear:  issued.ExpYear,
		Status:   "active",
	})
}

// NOTE: This endpoint does not verify that the caller owns the requested account.
// Caller authentication is a systemic concern for the entire API and not scoped to this handler.

// HandleCardDetails handles GET /issuing/card/details?account=kshs_...
func (ic *IssuingController) HandleCardDetails(w http.ResponseWriter, r *http.Request) {
	account := r.URL.Query().Get("account")

	// 1. Validate account param
	if !utils.ValidateAddress(account) {
		ErrBadrequest(w, r, "invalid_account")
		return
	}

	// 2. Look up card
	card, err := ic.CardRepo.GetCardByAccount(account)
	if err != nil {
		klog.Errorf("HandleCardDetails: GetCardByAccount error for %s: %v", account, err)
		ErrInternalServerError(w, r, "db_error")
		return
	}
	if card == nil {
		render.Status(r, http.StatusNotFound)
		render.JSON(w, r, &ErrorResponse{Error: "card_not_found"})
		return
	}

	// 3. Fetch postal code from stripe_addresses using card.AddressID
	var addrRow struct{ PostalCode string }
	result := ic.AddressRepo.DB.Raw(
		"SELECT postal_code FROM stripe_addresses WHERE id = ?", card.AddressID,
	).Scan(&addrRow)
	if result.Error != nil {
		klog.Errorf("HandleCardDetails: address lookup error for addressID %s: %v", card.AddressID, result.Error)
		ErrInternalServerError(w, r, "db_error")
		return
	}
	if addrRow.PostalCode == "" {
		klog.Errorf("HandleCardDetails: no address found for addressID %s", card.AddressID)
		ErrInternalServerError(w, r, "address_not_found")
		return
	}

	// 4. Fetch sensitive card details from Stripe
	details, err := stripeissuing.GetCardDetails(card.StripeCardID)
	if err != nil {
		klog.Errorf("HandleCardDetails: GetCardDetails error for cardID %s: %v", card.StripeCardID, err)
		ErrInternalServerError(w, r, "stripe_error")
		return
	}

	// 5. Return card details with postal code from DB
	render.Status(r, http.StatusOK)
	render.JSON(w, r, &cardDetailsResponse{
		Number:     details.Number,
		CVC:        details.CVC,
		ExpMonth:   details.ExpMonth,
		ExpYear:    details.ExpYear,
		PostalCode: addrRow.PostalCode,
	})
}
