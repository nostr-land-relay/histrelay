let signInButton = document.getElementById("sign-in")
let statusBox = document.getElementById("statusbox")
let defaultRelays = [
    "wss://hist.nostr.land",
    "wss://nostr.land",
    "wss://relay.damus.io",
    "wss://nos.lol",
    "wss://nostr.mutinywallet.com"
]

signInButton.addEventListener("click", async () => {
    if (!window.nostr) {
        signInButton.disabled = true
        signInButton.innerText = "no extension detected"
        return
    }
    let pk
    try {
        pk = await nostr.getPublicKey()
    } catch (e) {
        return alert("error: " + e)
    }
    loggedIn(pk)
})

async function loggedIn(pk) {
    signInButton.style.display = "none"
    statusBox.style.display = ""
    statusBox.innerText = "connecting to relay"
    let relay = await NostrTools.Relay.connect("wss://hist.nostr.land")
    statusBox.innerText = "getting events"
    let events = []
    const sub = relay.subscribe([
        {
            authors: [pk],
            kinds: [0, 3]
        },
    ], {
        onevent(event) {
            events.push(event)
        },
        oneose() {
            statusBox.innerText = "done"
            showList(events)
            sub.close()
        }
    })
}

function showList(list) {
    let kinds = [[0, "profiles"], [3, "contact lists"]]
    ;[...statusBox.childNodes].forEach(el => el.remove())
    kinds.forEach(([kind, titlestr]) => {
        let title = document.createElement("h3")
        title.innerText = titlestr + " (kind " + kind + ")"
        statusBox.appendChild(title)
        let kindEvents = list.filter(el => el.kind === kind)
        if (kindEvents.length === 0) {
            let p = document.createElement("p")
            p.innerText = "none found - add wss://hist.nostr.land"
            statusBox.appendChild(p)
        } else {
            kindEvents = kindEvents.sort((a, b) => b.created_at - a.created_at)
            kindEvents = kindEvents.slice(0, 15)
            let table = document.createElement("table")
            let entries = [
                {row: ["date", "description", "restore"], evt: null},
                ...kindEvents.map(el => {
                    let desc = "unknown"
                    if (el.kind === 0) {
                        let data
                        try {
                            data = JSON.parse(el.content)
                        } catch (e) {
                            desc = "corrupted event"
                            console.error(e)
                        }
                        if (data) {
                            desc = `name: ${data?.name || "none"}, nip05: ${data?.nip05 || "none"}`
                        }
                    } else if (el.kind === 3) {
                        desc = `following: ${el.tags.reduce((a, b) => a + Number(b?.[0] === "p"), 0)} people, ${el.tags.reduce((a, b) => a + Number(b?.[0] === "t"), 0)} hashtags`
                    }
                    let d = new Date(el.created_at * 1000)
                    return {
                        row: [d.toLocaleDateString() + " " + d.toLocaleTimeString(), desc, Symbol.for("histrelay:restore")],
                        evt: el
                    }
                })
            ]
            table.classList.add("fw")
            entries.forEach((el, i) => {
                let tr = document.createElement("tr")
                el.row.forEach((el2, j) => {
                    let td = document.createElement(i === 0 ? "th" : "td")
                    if (el2 === Symbol.for("histrelay:restore")) {
                        td.addEventListener("click", () => {
                            restore(el.evt)
                        })
                        td.innerText = "restore"
                        td.classList.add("restore")
                    } else {
                        td.innerText = el2
                    }
                    if (j === 0) {
                        if (i !== 0) td.classList.add("mono")
                        td.classList.add("w-mc")
                    }
                    tr.appendChild(td)
                })
                table.appendChild(tr)
            })
            statusBox.appendChild(table)
        }
    })
}

async function restore(evt) {
    evt = structuredClone(evt)
    evt.created_at = Math.floor(Date.now() / 1000)
    evt.id = NostrTools.getEventHash(evt)
    delete evt.sig
    let relaysObj
    try {
        relaysObj = await nostr.getRelays()
    } catch (e) {
        alert("error getting relays: " + e)
        return
    }
    let signed
    try {
        signed = await nostr.signEvent(evt)
    } catch (e) {
        alert("error signing: " + e)
        return
    }
    let relays = [...new Set([...defaultRelays, ...Object.entries(relaysObj)])]
    ;[...statusBox.childNodes].forEach(el => el.remove())
    let title = document.createElement("h3")
    title.innerText = "broadcast status"
    let table = document.createElement("table")
    let relayStatuses = {}
    ;["relay", ...relays].forEach((relay, i) => {
        let tr = document.createElement("tr")
        let td1 = document.createElement(i === 0 ? "th" : "td")
        let td2 = document.createElement(i === 0 ? "th" : "td")
        td1.innerText = relay
        td2.innerText = i === 0?"status":"connecting..."
        tr.appendChild(td1)
        tr.appendChild(td2)
        if (i !== 0) {
            relayStatuses[relay] = td2
        }
        table.appendChild(tr)
    })
    statusBox.appendChild(table)
    relays.forEach(async el => {
        let relay
        try {
            relay = await NostrTools.Relay.connect(el)
        } catch(e) {
            console.error(e)
            relayStatuses[el].innerText = "error connecting"
            return
        }
        relayStatuses[el].innerText = "sending event"
        let interval = setTimeout(() => {
            relayStatuses[el].innerText = "timed out"
        }, 5000)
        try {
            await relay.publish(signed)
            relayStatuses[el].innerText = "done"
        } catch(e) {
            relayStatuses[el].innerText = e
        }
        clearInterval(interval)
    })
}