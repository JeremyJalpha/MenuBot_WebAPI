package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type OrderData struct {
	OrderID       string
	PfPaymentID   string
	PaymentStatus string
	ItemName      string
}

func PaymentReturnHandler(tpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tpl.ExecuteTemplate(w, tpl.Name(), "")
	}
}

func PaymentCancelHandler(tpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tpl.ExecuteTemplate(w, tpl.Name(), "")
	}
}

func compileOrderData(r *http.Request) (OrderData, error) {
	// Extract the orderID from the URL
	orderID := r.URL.Query().Get("m_payment_id")
	pfPaymentID := r.URL.Query().Get("pf_payment_id")
	paymentStatus := r.URL.Query().Get("payment_status")
	itemName := r.URL.Query().Get("item_name")

	// Collect names of missing required fields
	var missingFields []string
	if orderID == "" {
		missingFields = append(missingFields, "m_payment_id")
	}
	if pfPaymentID == "" {
		missingFields = append(missingFields, "pf_payment_id")
	}
	if paymentStatus == "" {
		missingFields = append(missingFields, "payment_status")
	}
	if itemName == "" {
		missingFields = append(missingFields, "item_name")
	}

	// If any required fields are missing, return an error
	if len(missingFields) > 0 {
		return OrderData{}, fmt.Errorf("missing required order data: %s", strings.Join(missingFields, ", "))
	}

	orderData := OrderData{
		OrderID:       orderID,
		PfPaymentID:   pfPaymentID,
		PaymentStatus: paymentStatus,
		ItemName:      itemName,
	}

	return orderData, nil
}

func PaymentNotifyHandler(passPhrase, pfHost string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Respond to the payment notification
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte("Success"))
		if err != nil {
			log.Println("error writing response: ", err)
		}

		// Read the OrderID from The URL then pass it to SetCartFromDB
		itemID := r.URL.Query().Get("item_name")
		log.Println(strings.TrimPrefix(itemID, ItemNamePrefix))

		orderData, err := compileOrderData(r)
		if err != nil {
			log.Printf("Post payment check: compiling order data from payFast response failed: %v", err)
			return
		}

		orderMap := orderDataToMap(orderData)
		summedOrderData := checkPaymentResult(orderMap)

		if !pfValidSignature(orderMap, summedOrderData, passPhrase) {
			log.Printf("Post payment check: Signature validity test failed - payment gateway data: %v", orderData)
		}
		if !pfValidIP(r.Host) {
			log.Printf("Post payment check: Server IP test failed - payment gateway data: %v", orderData)
		}
		if pfValidServerConfirmation(summedOrderData, pfHost) {
			log.Printf("Post payment check: Server confirmation test failed - payment gateway data: %v", orderData)
		}
	}
}

func orderDataToMap(orderData OrderData) map[string]string {
	return map[string]string{
		"m_payment_id":   orderData.OrderID,
		"pf_payment_id":  orderData.PfPaymentID,
		"payment_status": orderData.PaymentStatus,
		"item_name":      orderData.ItemName,
	}
}

func checkPaymentResult(orderData map[string]string) string {
	// Strip any slashes in data (not needed in Go)
	for key, val := range orderData {
		orderData[key] = val
	}

	// Convert posted variables to a string
	var summedOrderData string
	for key, val := range orderData {
		if key != "signature" {
			summedOrderData += key + "=" + url.QueryEscape(val) + "&"
		} else {
			break
		}
	}

	return strings.TrimSuffix(summedOrderData, "&")
}

func pfValidSignature(orderData map[string]string, summedOrderData, passPhrase string) bool {
	var tempParamString string
	if passPhrase == "" {
		tempParamString = summedOrderData
	} else {
		tempParamString = summedOrderData + "&passphrase=" + url.QueryEscape(passPhrase)
	}

	hash := md5.New()
	hash.Write([]byte(tempParamString))
	calculatedSignature := hex.EncodeToString(hash.Sum(nil))

	return orderData["signature"] == calculatedSignature
}

func pfValidIP(referrerURL string) bool {
	// Valid hosts
	validHosts := []string{
		"www.payfast.co.za",
		"sandbox.payfast.co.za",
		"w1w.payfast.co.za",
		"w2w.payfast.co.za",
	}

	// Get IP addresses for valid hosts
	var validIps []string
	for _, pfHostname := range validHosts {
		ips, err := net.LookupIP(pfHostname)
		if err == nil {
			for _, ip := range ips {
				validIps = append(validIps, ip.String())
			}
		}
	}

	// Remove duplicates
	uniqueIps := make(map[string]bool)
	for _, ip := range validIps {
		uniqueIps[ip] = true
	}

	// Get IP address from referrer URL
	referrerHost := strings.TrimPrefix(referrerURL, "http://")
	referrerHost = strings.TrimPrefix(referrerHost, "https://")
	referrerHost = strings.Split(referrerHost, "/")[0]
	referrerIp, err := net.LookupIP(referrerHost)
	if err != nil {
		return false
	}

	// Check if referrer IP is valid
	return uniqueIps[referrerIp[0].String()]
}

func pfValidServerConfirmation(summedOrderData, pfHost string) bool {
	url := fmt.Sprintf("https://%s/eng/query/validate", pfHost)

	client := &http.Client{}
	req, err := http.NewRequest("POST", url, strings.NewReader(summedOrderData))
	if err != nil {
		log.Printf("Error creating request: %v", err)
		return false
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Error sending request: %v", err)
		return false
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading response body: %v", err)
		return false
	}

	return string(body) == "VALID"
}
