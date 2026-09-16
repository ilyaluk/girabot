# girabot

> An alternative client for Lisbon's Gira bike sharing service.

## Use

Prod instance is running at [@BetterGiraBot](https://t.me/BetterGiraBot).

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

Gira moved to the VAIMOO platform in 2026. The bot now talks to three services:

- EMEL login, at `login.emel.pt`
- VAIMOO consumer API, at `emel-consumerapp.vaimoo.com`
- a public Firestore database that holds live station and bike state

Login is implemented in internal/vaimoo. EMEL still owns the credentials, so an
email and a password are traded for an EMEL token, that token for a single use
code, and the code for a VAIMOO session. Access tokens last 5 minutes, refresh
tokens 5 days, and every refresh rotates both.

The VAIMOO API serves the account, the wallet, subscriptions, trips and trip
ratings. Unlocking a bike is what starts a trip, in a single call naming the
bike by its communication id. Authentication is the access token in the
Authorization header, with no Bearer prefix. Requests also carry an AppId header
and a userId query parameter.

Stations and bikes are not in that API. The official app reads them straight out
of a Firestore database that allows unauthenticated reads with the app's public
API key, which internal/firestore queries over the REST API. Gira shares the
database with other VAIMOO cities, so every query is scoped to the Gira tenant.

internal/gira puts both sources together and is where the bot's logic lives.
There is no push channel for trips any more, so a running trip is polled. A bike
publishes its own state to Firestore sooner than the trip endpoint reacts, which
is used to poll harder around the moment a ride looks finished, but only the trip
endpoint decides whether a trip is running.
