# girabot

> An alternative client for Lisbon's Gira bike sharing service.

## Install

```sh
go install github.com/ilyaluk/girabot@latest
export TOKEN=<your telegram bot token>
girabot -h
```

## Details

Your usual telegram bot. SQLite storage. telebot is used for telegram API.

In current setup bot is expected to be run behind a reverse proxy, which is responsible for SSL termination.

Set -domain and -url-prefix accordingly, and confugure your reverse proxy to forward requests to the bot port.

## Gira API details

Gira has two API endpoints:

- Auth API
- GraphQL API

Auth API is implemented in internal/giraauth package. It is used to get a JWT token for GraphQL API.
It exchanges login-password for an access and refresh tokens pair. Refresh token is valid for 7 days, while access token for 2 minutes.

GraphQL API is implemented in internal/gira package. The main logic lies here.
GraphQL API is what you would expect and has introspection, so it is easy to understand what queries it supports. Authentication is done via standard HTTP authorization/bearer header.

Beware that APIs return errors half of the time, so be prepared to retry requests.
