package main

const messageHello = `
👋 Hello! I'm BetterGiraBot, an alternative client for Gira bike sharing service.

I don't provide all the features of the official app, but I do some things better and more reliable.
Here's what I can do:
- 📍 Show the nearest bike stations
- 🚲 List available bikes at a station
- 🔓 Unlock bikes
- ⭐️ Mark your favorite stations
- ℹ️ Show current trip status
- 📈 Rate your trips

You still need the official app to register and purchase subscription, but I'm great for everyday use.

This bot is not affiliated with Gira or EMEL in any way. This bot is provided as-is, without any warranty. Use at your own risk.

For any questions, please contact @ilyaluk.
`

const messageLogin = `
Now, you need to log in. For that, I'll need your email and password for Gira app.
That sounds scary, but I won't save your credentials, pinky promise.
I'll only use them to log in to Gira API and fetch the access token, which I will store and use to access Gira API on your behalf.
Email and password will not be stored in my database, and I'll forget them as soon as I log in.

Please send me your email.
`

const messagePassword = `
Great! Now, please send me your password.
I'll remove it from the message history after login.
`

const messageHelp = `
How to use this bot:

📍 Send me a location, and I'll show you the nearest bike stations. You can send me your current location using convenient menu button, or any one via 📎 → Location.
🅿️ Tap on a station to see available bikes.
⚡️ – electric bikes, ⚙️ – regular bikes, 💯 – full battery

📋 Tap on a bike to open unlock menu.

ℹ️ I will show you the current trip status (with some lag), and after returning the bike, I will show you the trip summary.
🔚 While you have active trip, you can also send me locations to see the nearest stations. I will show you how many docks are available. The station information is delayed a bit, so the dock might end up being taken.
‼️ At the moment I can't help you to pay for the trip, so you'll have to do it in the official app.
📈 Also, I'll ask you to rate the bike after the trip.

⭐️ You can save your favorite stations, and I'll list them on request (and include names in listings).
`

//💸 If required, you can pay for the trip using buttons in the chat.

const messageFeedback = `
☺️ Hope you're enjoying the bot! It's a small pet project, and I'd love to hear your feedback.
Feel free to drop me a message at @ilyaluk.
`

const messageDonate = `
🥰 If you liked the bot, you can support it by donating. It will help me to keep the bot running and improve it.
Revolut: [@ilyaluk](https://revolut.me/ilyaluk/) (from any bank card)

💌 Feel free to drop me a message at @ilyaluk if you have any questions or suggestions.

Won't bother you with this message anymore. 🤗
`
