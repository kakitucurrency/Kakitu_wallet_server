// kcb/query.go
package kcb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type queryReqHeader struct {
	MessageID           string `json:"messageId"`
	FeatureCode         string `json:"featureCode"`
	FeatureName         string `json:"featureName"`
	ServiceCode         string `json:"serviceCode"`
	ServiceName         string `json:"serviceName"`
	ServiceSubCategory  string `json:"serviceSubCategory"`
	MinorServiceVersion string `json:"minorServiceVersion"`
	ChannelCode         string `json:"channelCode"`
	ChannelName         string `json:"channelName"`
	RouteCode           string `json:"routeCode"`
	TimeStamp           string `json:"timeStamp"`
	ServiceMode         string `json:"serviceMode"`
	SubscribeEvents     string `json:"subscribeEvents"`
	CallBackURL         string `json:"callBackURL"`
}

type queryRequest struct {
	Header         queryReqHeader `json:"header"`
	RequestPayload struct {
		TransactionInfo struct {
			PrimaryData struct {
				BusinessKey     string `json:"businessKey"`
				BusinessKeyType string `json:"businessKeyType"`
			} `json:"primaryData"`
			AdditionalDetails struct {
				CompanyCode string `json:"companyCode"`
			} `json:"additionalDetails"`
		} `json:"transactionInfo"`
	} `json:"requestPayload"`
}

type queryResponse struct {
	Header struct {
		StatusCode string `json:"statusCode"`
	} `json:"header"`
	ResponsePayload struct {
		TransactionInfo struct {
			PrimaryData struct {
				TransactionStatus string `json:"transactionStatus"`
			} `json:"primaryData"`
		} `json:"transactionInfo"`
	} `json:"responsePayload"`
}

// QueryTransaction queries the status of a cashout by our transactionReference.
// Returns "SUCCESS", "FAILED", or "PENDING".
func QueryTransaction(token, txRef, companyCode string) (string, error) {
	messageID := strings.ReplaceAll(uuid.New().String(), "-", "")

	var req queryRequest
	req.Header = queryReqHeader{
		MessageID:           messageID,
		FeatureCode:         "101",
		FeatureName:         "FinancialInquiries",
		ServiceCode:         "1004",
		ServiceName:         "TransactionInfo",
		ServiceSubCategory:  "ACCOUNT",
		MinorServiceVersion: "1.0",
		ChannelCode:         "206",
		ChannelName:         "ibank",
		RouteCode:           "001",
		TimeStamp:           time.Now().UTC().Format(time.RFC3339Nano),
		ServiceMode:         "sync",
		SubscribeEvents:     "1",
		CallBackURL:         "",
	}
	req.RequestPayload.TransactionInfo.PrimaryData.BusinessKey = txRef
	req.RequestPayload.TransactionInfo.PrimaryData.BusinessKeyType = "FT.REF"
	req.RequestPayload.TransactionInfo.AdditionalDetails.CompanyCode = companyCode

	body, err := json.Marshal(req)
	if err != nil {
		return "PENDING", fmt.Errorf("marshaling query request: %w", err)
	}

	httpReq, err := http.NewRequest(
		http.MethodPost,
		BaseURL()+"/v1/core/t24/querytransaction/1.0.0/api/transactioninfo",
		bytes.NewBuffer(body),
	)
	if err != nil {
		return "PENDING", fmt.Errorf("building query request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	// Use direct map assignment to preserve case — http.Header.Set canonicalises header names.
	httpReq.Header["messageID"] = []string{messageID}
	httpReq.Header["channelCode"] = []string{"206"}

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return "PENDING", fmt.Errorf("calling KCB query: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "PENDING", nil // treat read failure as still pending
	}

	var qResp queryResponse
	if err := json.Unmarshal(respBody, &qResp); err != nil {
		return "PENDING", nil // treat parse failure as still pending
	}

	status := qResp.ResponsePayload.TransactionInfo.PrimaryData.TransactionStatus
	switch status {
	case "SUCCESS":
		return "SUCCESS", nil
	case "FAILED":
		return "FAILED", nil
	default:
		return "PENDING", nil
	}
}
