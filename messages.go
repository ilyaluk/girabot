package main

const messageHello = `
👋 Hello! I'm BetterGiraBot, an alternative client for Gira bike sharing service.

I don't provide all the features of the official app, but I do some things better and more reliable. Here's what I can do:
- 📍 Show the nearest bike stations
- 🚲 List available bikes at a station
- 🔓 Unlock bikes
- ℹ️ Show current trip status
- 📈 Rate your trips
- ⭐️ Mark your favorite stations

You still need the official app to register and purchase subscription, but I'm great for everyday use.

🧑‍⚖️ _This bot is not affiliated with Gira or EMEL in any way. This bot is provided as-is, without any warranty. Use at your own risk._

✍ For any questions, please contact @ilyaluk.
`

const messageLogin = `
Now, you need to log in. For that, I'll need your email and password for Gira app.
That sounds scary, but I won't save your credentials, pinky promise.
I'll only use them to log in to Gira API and fetch the access token, which I will store and use to access Gira API on your behalf.
Password will not be stored in my database, and I'll forget email and password right after login.

Please send me your email. If you prefer, send email and password at once, on two separate lines.
`

const messagePassword = `
Great! Now, please send me your password.
I'll remove it from the message history after login.
`

const messageLoginLinkUsage = `
🔗 I can make a link that logs in with a single tap, handy for sharing an account or re-logging in later.

Send me ` + "`/loginlink <email> <password>`" + ` (password can go on the next line), and I'll reply with the link.
I'll delete your message right away, but the link itself is as good as the password, so share it carefully.
`

const messageHelp = `
How to use this bot:

📍 Send me a location, and I'll show you the nearest bike stations. You can share your location using convenient menu button, or any point via 📎 → Location.
🅿️ Tap on a station to see available bikes. Or just send station number to view it.
⚡️ – electric bikes, ⚙️ – regular bikes, 💯 – full battery

📋 Tap on a bike to open unlock menu.

ℹ️ I will show you the current trip status, and after returning the bike, I will show you the trip summary.
🔚 While you have active trip, you can also send me location, I will show you how many docks are available there. _The station information is delayed, so the dock might end up being taken._
💸 If required, you can pay for the trip using buttons in the chat _(not well-tested)_.
📈 Also, I'll ask you to rate the trip afterwards.

⭐️ You can name your favorite stations, I could list them, and include names in searches for convenience.

🤓 If neat keyboard disappeared, run /help. To re-login run /login.
🔗 /loginlink makes a one-tap login link out of an email and a password.
`

const messageFeedback = `
☺️ Hope you're enjoying the bot! It's a small pet project, and I'd love to hear your feedback.
Feel free to drop me a message at @ilyaluk.
`

const messageDonate = `
🥰 If you liked the bot, you can support it by donating. It will help me to keep the bot running and improve it.

💳 revolut.me/ilyaluk (from any bank card)

💌 Feel free to drop me a message at @ilyaluk if you have any questions or suggestions.

Won't bother you with this message anymore. 🤗
`

const messageRateTrip = `
📈 Please rate the trip.

Don't forget to submit.
`
