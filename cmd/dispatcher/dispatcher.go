package main

import (
	"context"
	"log"
	"sync"
	"time"

	_ "github.com/lib/pq"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"kannon.gyozatech.dev/generated/pb"
	"kannon.gyozatech.dev/generated/sqlc"
	"kannon.gyozatech.dev/internal/mailbuilder"
	"kannon.gyozatech.dev/internal/pool"

	"github.com/nats-io/jsm.go"
	"github.com/nats-io/nats.go"
)

type appConfig struct {
	NatsConn string `default:"nats://127.0.0.1:4222"`
}

func main() {
	godotenv.Load()

	var config appConfig
	err := envconfig.Process("app", &config)
	if err != nil {
		log.Fatal(err.Error())
	}

	db, err := sqlc.Conn()
	if err != nil {
		panic(err)
	}
	defer db.Close()

	pm, err := pool.NewSendingPoolManager(db)
	if err != nil {
		panic(err)
	}

	mb := mailbuilder.NewMailBuilder(db)

	nc, err := nats.Connect(config.NatsConn, nats.UseOldRequestStyle())
	if err != nil {
		logrus.Fatalf("Cannot connect to nats: %v\n", err)
	}
	mgr, err := jsm.New(nc)
	if err != nil {
		panic(err)
	}

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		handleErrors(mgr)
		wg.Done()
	}()
	go func() {
		handleDelivereds(mgr)
		wg.Done()
	}()
	go func() {
		dispatcherLoop(pm, mb, nc)
		wg.Done()
	}()
	wg.Wait()
}

func dispatcherLoop(pm pool.SendingPoolManager, mb mailbuilder.MailBulder, nc *nats.Conn) {
	for {
		emails, err := pm.PrepareForSend(100)
		if err != nil {
			logrus.Fatalf("cannot prepare for send: %v", err)
		}
		logrus.Debugf("Fetched %v emails\n", len(emails))
		for _, email := range emails {
			data, err := mb.PerpareForSend(email)
			if err != nil {
				logrus.Errorf("Cannot send email %v: %v", email.Email, err)
				continue
			}
			msg, err := proto.Marshal(&data)
			if err != nil {
				logrus.Errorf("Cannot send email %v: %v", email.Email, err)
				continue
			}
			err = nc.Publish("emails.sending", msg)
			if err != nil {
				logrus.Errorf("Cannot send message on nats: %v", err.Error())
				continue
			}
			logrus.Infof("[✅ accepted]: %v %v", data.To, data.MessageId)
		}
		logrus.Debugf("done sending emails")
		time.Sleep(1 * time.Second)
	}
}

func handleErrors(mgr *jsm.Manager) {
	con, err := mgr.LoadConsumer("kannon", "email-error")
	if err != nil {
		panic(err)
	}
	for {
		msg, err := con.NextMsgContext(context.Background())
		if err != nil {
			panic(err)
		}
		errMsg := pb.Error{}
		err = proto.Unmarshal(msg.Data, &errMsg)
		if err != nil {
			logrus.Errorf("cannot marshal message %v", err.Error())
		} else {
			logrus.Printf("[🛑 bump] %v %v - %v", errMsg.Email, errMsg.MessageId, errMsg.Msg)
		}
		msg.Ack()
	}
}

func handleDelivereds(mgr *jsm.Manager) {
	con, err := mgr.LoadConsumer("kannon", "email-delivered")
	if err != nil {
		panic(err)
	}
	for {
		msg, err := con.NextMsgContext(context.Background())
		if err != nil {
			panic(err)
		}
		deliveredMsg := pb.Delivered{}
		err = proto.Unmarshal(msg.Data, &deliveredMsg)
		if err != nil {
			logrus.Errorf("cannot marshal message %v", err.Error())
		} else {
			logrus.Printf("[🚀 delivered] %v %v", deliveredMsg.Email, deliveredMsg.MessageId)
		}
		msg.Ack()
	}
}
