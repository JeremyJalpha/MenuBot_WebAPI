package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"database/sql"

	_ "github.com/lib/pq"
	"google.golang.org/protobuf/proto"

	"github.com/joho/godotenv"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	mb "github.com/JeremyJalpha/MenuBotLib"
	"github.com/go-chi/chi/v5"
	"github.com/mdp/qrterminal"
)

// Example app.env file:
// DATABASE_URL=postgresql://postgres:************@localhost:5432/WhatsAppBot6?sslmode=disable
// PFHOST=https://sandbox.payfast.co.za/eng/process
// HOST_NUMBER=27000000000
// HOMEBASEURL=https://yourhomedomain.ngrok-free.app/
// MERCHANTID=XXXXXXXX
// MERCHANTKEY=*************
// PASSPHRASE=*************

const (
	catalogueID string = "Pig"

	prclstPreamble = "All fertilizer quoted per gram."

	isTest              = true
	whatsAppServer      = "s.whatsapp.net"
	staleMsgTimeOut int = 10
	pymntRtrnBase       = "payment_return"
	pymntCnclBase       = "payment_canceled"
	returnBaseURL       = "/" + pymntRtrnBase
	cancelBaseURL       = "/" + pymntCnclBase
	notifyBaseURL       = "/payment_notify"
	ItemNamePrefix      = "Order"
	isAutoInc           = false
)

type EnvVars struct {
	Pwd         string
	DBConn      string
	HostNumber  string
	HomebaseURL string
	MerchantId  string
	MerchantKey string
	Passphrase  string
	PfHost      string
}

// RemoveNonASCIICharacters removes non-ASCII characters, including non-breaking spaces
func RemoveNonASCIICharacters(s string) string {
	var builder strings.Builder
	for _, c := range s {
		if c <= 127 {
			builder.WriteRune(c)
		}
	}
	return builder.String()
}

func eventHandler(evt interface{}, c *whatsmeow.Client, db *sql.DB, prcList mb.Pricelist, checkoutInfo mb.CheckoutInfo, envvars EnvVars) {
	switch v := evt.(type) {
	case *events.Message:
		senderNumber := strings.Split(v.Info.Sender.ToNonAD().User, "@")[0]
		message := v.Message.GetConversation()
		msgCleaned := RemoveNonASCIICharacters(message)
		if senderNumber != envvars.HostNumber && isTest == false {
			convo := mb.NewConversationContext(db, senderNumber, msgCleaned, prcList, isAutoInc)
			convo.UserInfo.CellNumber = senderNumber
			botResp := mb.GetResponseToMsg(convo, db, checkoutInfo, isAutoInc)

			_, err := c.SendMessage(context.Background(), types.NewJID(senderNumber, whatsAppServer), &waProto.Message{Conversation: proto.String(botResp)})
			if err != nil {
				log.Printf("ReturnToUser Failed with: " + err.Error())
			}
		} else {
			log.Println("You sent a message:", message)
		}
	}
}

func getEnvVar(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("%s environment variable does not exist", name)
	}
	return value
}

// TODO: if WhatsApp token is stale app just exits silently without error or warning - please fix.
func main() {
	if err := godotenv.Load("app.env"); err != nil {
		log.Fatalf("Error loading .env file: %v", err)
	}

	envVars := EnvVars{
		DBConn:      getEnvVar("DATABASE_URL"),
		HostNumber:  getEnvVar("HOST_NUMBER"),
		HomebaseURL: getEnvVar("HOMEBASEURL"),
		MerchantId:  getEnvVar("MERCHANTID"),
		MerchantKey: getEnvVar("MERCHANTKEY"),
		Passphrase:  getEnvVar("PASSPHRASE"),
		PfHost:      getEnvVar("PFHOST"),
	}

	// Open the database connection
	db, err := sql.Open("postgres", envVars.DBConn)
	if err != nil {
		log.Fatal("Error opening database: ", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Fatal("Error closing database: ", err)
		}
	}()

	// Get the current working directory
	envVars.Pwd, err = os.Getwd()
	if err != nil {
		log.Fatal("Failed to get current directory: ", err)
	}

	// Construct the path to the template file
	pymntRtrnTplPath := filepath.Join(envVars.Pwd, "templates", pymntRtrnBase+".html")
	pymntCnclTplPath := filepath.Join(envVars.Pwd, "templates", pymntCnclBase+".html")

	pymntRtrnTpl := template.Must(template.ParseFiles(pymntRtrnTplPath))
	pymntCnclTpl := template.Must(template.ParseFiles(pymntCnclTplPath))

	r := chi.NewRouter()

	dbLog := waLog.Stdout("Database", "DEBUG", true)
	// Make sure you add appropriate DB connector imports, e.g. github.com/mattn/go-sqlite3 for SQLite
	container, err := sqlstore.New("postgres", envVars.DBConn, dbLog)
	if err != nil {
		panic(err)
	}
	// If you want multiple sessions, remember their JIDs and use .GetDevice(jid) or .GetAllDevices() instead.
	deviceStore, err := container.GetFirstDevice()
	if err != nil {
		panic(err)
	}
	clientLog := waLog.Stdout("Client", "DEBUG", true)
	chatClient := whatsmeow.NewClient(deviceStore, clientLog)
	checkoutInfo := mb.CheckoutInfo{
		ReturnURL:      envVars.HomebaseURL + returnBaseURL,
		CancelURL:      envVars.HomebaseURL + cancelBaseURL,
		NotifyURL:      envVars.HomebaseURL + notifyBaseURL,
		MerchantId:     envVars.MerchantId,
		MerchantKey:    envVars.MerchantKey,
		Passphrase:     envVars.Passphrase,
		HostURL:        envVars.PfHost,
		ItemNamePrefix: ItemNamePrefix,
	}
	log.Println("Loading pricelist from DB...")
	ctlgItms, err := mb.GetCatalogueItemsFromDB(db, catalogueID)
	if err != nil {
		log.Fatal("Error reading pricelist from database: ", err)
	}
	ctlgSelections := mb.CmpsCtlgSlctnsFromCtlgItms(ctlgItms)
	prclist := mb.Pricelist{
		PrlstPreamble: prclstPreamble,
		Catalogue:     ctlgSelections,
	}
	chatClient.AddEventHandler(func(evt interface{}) {
		eventHandler(evt, chatClient, db, prclist, checkoutInfo, envVars)
	})

	// Define routes
	r.Get(returnBaseURL, PaymentReturnHandler(pymntRtrnTpl))
	r.Get(notifyBaseURL, PaymentNotifyHandler(envVars.Passphrase, envVars.PfHost))
	r.Get(cancelBaseURL, PaymentCancelHandler(pymntCnclTpl))

	if chatClient.Store.ID == nil {
		// No ID stored, new login
		qrChan, _ := chatClient.GetQRChannel(context.Background())
		err = chatClient.Connect()
		if err != nil {
			panic(err)
		}
		for evt := range qrChan {
			if evt.Event == "code" {
				// Render the QR code here
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			} else {
				fmt.Println("Login event:", evt.Event)
			}
		}
	} else {
		// Already logged in, just connect
		err = chatClient.Connect()
		if err != nil {
			panic(err)
		}
	}

	// Listen to Ctrl+C (you can also do something else that prevents the program from exiting)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	chatClient.Disconnect()
}
