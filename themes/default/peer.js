// 编辑页「发现的服务」面板里的「查找节点」：找同网段里也跑着这个导航页的机器，
// 点「加入」把它的地址写进上面的地址框，跟别的改动一起「保存到 JSON」。
// 服务端在 internal/peer。跟 admin.js 只有一处接触：往地址框里写完发一个 input 事件，
// admin.js 照常记成「有未保存的改动」。不想要这个功能，删掉这个文件和 admin.html 里那一块即可。

{
  const hostsBox = document.getElementById("svc-hosts")
  const list = document.getElementById("peer-list")
  const status = document.getElementById("peer-status")
  const buttons = [document.getElementById("peer-find"), document.getElementById("peer-sweep")]
  const tell = (text, kind = "") => { status.textContent = text; status.dataset.kind = kind }

  const hosts = () => hostsBox.value.split(/[\s,，]+/).filter(Boolean)

  const add = (host, btn) => {
    if (!hosts().includes(host)) {
      hostsBox.value = (hostsBox.value.trimEnd() + "\n" + host).trimStart() + "\n"
      hostsBox.dispatchEvent(new Event("input", { bubbles: true })) // 让 admin.js 记成未保存
    }
    btn.disabled = true
    btn.textContent = "已在地址里"
  }

  const render = (peers) => {
    const have = hosts()
    list.replaceChildren(...peers.map((p) => {
      const li = document.createElement("li")
      const name = document.createElement("span")
      name.className = "peer__name"
      name.textContent = `${p.name || "（没报名字）"}  ${p.host}:${p.port}`
      const btn = document.createElement("button")
      btn.type = "button"
      btn.className = "add"
      btn.onclick = () => add(p.host, btn)
      if (have.includes(p.host)) { btn.disabled = true; btn.textContent = "已在地址里" } else btn.textContent = "加入"
      li.append(name, btn)
      // 网页端口不在服务端的 NodePorts 里：加进去以后问不到它，只能照常扫描
      if (!p.asked) {
        const note = document.createElement("span")
        note.className = "peer__note"
        note.textContent = `网页端口 ${p.port} 不是默认的，加入后会按普通地址扫描`
        li.append(note)
      }
      return li
    }))
  }

  const run = async (url, busy, none) => {
    for (const b of buttons) b.disabled = true
    list.replaceChildren()
    tell(busy)
    try {
      const res = await fetch(url, { method: "POST" })
      const text = (await res.text()).trim()
      if (!res.ok) return tell(text || `查找失败（HTTP ${res.status}）`, "bad")
      const peers = JSON.parse(text)
      render(peers)
      tell(peers.length ? `找到 ${peers.length} 个节点` : none, peers.length ? "ok" : "")
    } catch (err) {
      tell("查找失败：" + err.message, "bad")
    } finally {
      for (const b of buttons) b.disabled = false
    }
  }

  buttons[0].onclick = () => run("/api/peers/find", "正在广播…",
    "同网段里没找到。对方开了防火墙、或者路由器拦了广播的话，试试「逐个询问网段」")
  buttons[1].onclick = () => run("/api/peers/sweep", "正在逐个询问网段里的每个 IP，要十几秒…",
    "网段里没找到。不在同一网段的机器，直接把地址填进上面")
}
