package gira

import (
	"log"

	"github.com/hasura/go-graphql-client"
)

func NewSubscriptionClient(accessToken string) *graphql.SubscriptionClient {
	c := graphql.NewSubscriptionClient("wss://apigira.emel.pt/graphql")

	var subscription struct {
		ServerDate struct {
			DateType struct {
				Date string
			}
		} `graphql:"serverDate(_access_token: $_access_token)"`
	}

	if _, err := c.Subscribe(&subscription, map[string]any{
		"_access_token": accessToken,
	}, func(msg []byte, err error) error {
		log.Println("subscription cb:", string(msg), err)
		return nil
	}); err != nil {
		log.Println("subscription:", err)
		return nil
	}

	go c.Run()

	return c
}
