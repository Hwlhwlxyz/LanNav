// 编辑页的全部行为。页面本身由 Go 模板渲染好，这里只负责四件事：
// 切分组、增删移动、把 DOM 拼回 JSON、提交。
// 没有任何数据副本 —— 页面上的输入框就是唯一的真相，保存时现场读一遍。

const main = document.querySelector("main")
const nav = document.getElementById("nav")
const statusEl = document.getElementById("status")
const rowTpl = document.getElementById("row-tpl").content.querySelector(".row")
const groupTpl = document.getElementById("group-tpl").content.querySelector(".ed-group")
const navTpl = document.getElementById("nav-tpl").content.querySelector(".side__link")

const sections = () => [...main.querySelectorAll(".ed-group")]
let dirty = false

// —— 改动状态 ——
// 保存是显式的，所以必须让「还没落盘」这件事一直看得见，离开前再拦一次。
const markDirty = () => {
  if (dirty) return          // 已经是脏的就别再写一遍 status，否则读屏每敲一个字就播报一次
  dirty = true
  document.body.dataset.dirty = ""
  say("有未保存的改动")
  gateIcons()
}
const say = (text, kind = "") => { statusEl.textContent = text; statusEl.dataset.kind = kind }

addEventListener("beforeunload", (e) => { if (dirty) e.preventDefault() })
main.addEventListener("input", markDirty)
document.getElementById("title").addEventListener("input", markDirty)
document.getElementById("open").addEventListener("input", markDirty)   // 顶栏不在 main 里，单独挂

// —— 分组切换 ——
// 和导航页同一套：分组全在 DOM 里，切换只改 is-active。
// 「发现的服务」「网站图标」这些设置面板也走这一套，它们的 data-group 是分组用不了的 id，不会撞。
const panels = [...main.querySelectorAll(".ed-panel")]
const panelLinks = [...document.querySelectorAll(".panel-link")]
const show = (id) => {
  for (const el of [...sections(), ...nav.children, ...panels, ...panelLinks])
    el.classList.toggle("is-active", el.dataset.group === id)
}

for (const a of panelLinks) a.onclick = (e) => {
  e.preventDefault()
  show(a.dataset.group)
}

nav.onclick = (e) => {
  const a = e.target.closest(".side__link")
  if (!a) return
  e.preventDefault()
  show(a.dataset.group)
}

// 侧边栏跟着分组的名称、图标、条数走。分组 id 改了也要同步，
// 否则点侧边栏会找不到对应的 section。
const syncNav = () => {
  const secs = sections()
  const links = [...nav.children]
  // 按下标配对，不按 id 查找 —— id 是用户随时在改、也可能敲重的字段，不能当键用。
  // 删掉一个分组后 section 少一个，多出来的那个入口直接摘掉。
  for (const a of links.slice(secs.length)) a.remove()
  secs.forEach((s, i) => {
    const a = links[i]
    if (!a) return
    s.dataset.group = s.querySelector(".f-gid").value.trim()
    a.dataset.group = s.dataset.group
    a.querySelector(".side__icon").textContent = s.querySelector(".f-gicon").value.trim() || "📁"
    a.querySelector(".nav-name").textContent = s.querySelector(".f-gname").value.trim() || "未命名分组"
    a.querySelector(".nav-count").textContent = s.querySelectorAll(".row").length
  })
}
main.addEventListener("input", syncNav)

// —— 增删与排序 ——
// 排序用上移/下移按钮而不是拖拽：按钮本身就是键盘可用的，
// 拖拽还得额外再配一套键盘方案。保存时 order 由服务端按顺序重排。
const swap = (el, dir) => {
  const sib = dir < 0 ? el.previousElementSibling : el.nextElementSibling
  if (sib) dir < 0 ? sib.before(el) : sib.after(el)
}

main.onclick = (e) => {
  const btn = e.target.closest("button")
  if (!btn) return
  const row = btn.closest(".row")
  const sec = btn.closest(".ed-group")

  if (btn.classList.contains("add-item")) {
    const fresh = rowTpl.cloneNode(true)
    sec.querySelector(".rows").append(fresh)
    fresh.querySelector(".f-name").focus()
  } else if (btn.classList.contains("move")) {
    swap(row, +btn.dataset.dir)
  } else if (btn.classList.contains("del")) {
    const name = row.querySelector(".f-name").value.trim()
    if (name && !confirm(`删除书签「${name}」？`)) return
    row.remove()
  } else if (btn.classList.contains("gmove")) {
    const i = sections().indexOf(sec)      // 先记下标，section 一动就找不着了
    swap(sec, +btn.dataset.dir)
    swap(nav.children[i], +btn.dataset.dir)
  } else if (btn.classList.contains("gdel")) {
    if (sections().length === 1) return say("至少要保留一个分组", "bad")
    const name = sec.querySelector(".f-gname").value.trim()
    if (!confirm(`删除分组「${name}」和它下面的 ${sec.querySelectorAll(".row").length} 条书签？`)) return
    sec.remove()
    syncNav()
    show(sections()[0].dataset.group)
  } else return

  syncNav()
  markDirty()
}

document.getElementById("add-group").onclick = () => {
  const sec = groupTpl.cloneNode(true)
  let n = sections().length + 1
  while (sections().some((s) => s.dataset.group === "group-" + n)) n++   // 删过分组后不要重号
  const id = "group-" + n
  sec.dataset.group = id
  sec.querySelector(".f-gid").value = id
  main.append(sec)

  const a = navTpl.cloneNode(true)
  a.dataset.group = id
  nav.append(a)

  syncNav()
  show(id)
  markDirty()
  sec.querySelector(".f-gname").focus()
}

// —— DOM → JSON ——
// order 不在这里算：页面上的先后顺序就是顺序，服务端按提交顺序重新编号。
const v = (el, sel) => el.querySelector(sel).value.trim()

const collect = () => ({
  title: document.getElementById("title").value.trim(),
  open: document.getElementById("open").value,
  favicons: document.getElementById("favicons").checked,
  groups: sections().map((g) => ({
    id: v(g, ".f-gid"),
    name: v(g, ".f-gname"),
    icon: v(g, ".f-gicon"),
    hidden: g.querySelector(".f-ghidden").checked,
    items: [...g.querySelectorAll(".row")].map((r) => ({
      name: v(r, ".f-name"),
      url: v(r, ".f-url"),
      description: v(r, ".f-desc"),
      open: r.querySelector(".f-open").value,   // 空串 = 跟随默认
      icon: v(r, ".f-icon"),
      hidden: r.querySelector(".f-hidden").checked,
    })),
  })),
  // 地址一行一个；顺手也认逗号和空格分隔，粘贴进来的一串不用先手动换行。
  // 去空白、去重交给服务端规整。
  scan: {
    hosts: document.getElementById("svc-hosts").value.split(/[\s,，]+/).filter(Boolean),
    ports: document.getElementById("svc-ports").value.trim(),
  },
})

// —— 保存 ——
const saveBtn = document.getElementById("save")

saveBtn.onclick = async () => {
  // 先让浏览器跑一遍原生校验（required、type=url、分组 id 的 pattern），
  // 报错的字段自己会高亮，不用另写一套提示。
  const bad = [document.getElementById("title"), ...main.querySelectorAll("input")].find((i) => !i.checkValidity())
  if (bad) {
    const sec = bad.closest(".ed-group, .ed-panel")   // 标题框不在任何分组里，就不用切
    if (sec) show(sec.dataset.group)
    bad.reportValidity()
    return
  }

  saveBtn.disabled = true
  say("保存中…")
  try {
    const res = await fetch("/api/data", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ stamp: document.body.dataset.stamp, data: collect() }),
    })
    const text = (await res.text()).trim()
    if (!res.ok) { say(text || `保存失败（HTTP ${res.status}）`, "bad"); return }
    // 拿回新指纹，页面可以接着改，不用刷新
    document.body.dataset.stamp = JSON.parse(text).stamp
    dirty = false
    delete document.body.dataset.dirty
    gateIcons()
    say(`已保存 ${new Date().toLocaleTimeString("zh-CN", { hour12: false })}`, "ok")
    // 同一个浏览器里开着的导航页收到就自己刷新。跨标签页只能传消息、
    // 不能直接操控对方，所以是「广播一声让它自己刷」而不是「我去刷它」。
    // 手机上那个标签页收不到 —— 广播不过网络。
    new BroadcastChannel("nav").postMessage("saved")
  } catch (err) {
    say("保存失败：" + err.message, "bad")
  } finally {
    saveBtn.disabled = false
  }
}

// Ctrl+S（Mac 上 ⌘S）等于点「保存到 JSON」。挂在 window 上，焦点在哪个输入框里都管用；
// 一律 preventDefault，不然浏览器会弹出「网页另存为」。
// 走 saveBtn.click() 而不是直接调函数：保存中按钮是禁用的，click() 对禁用按钮无效，
// 连按几下也不会重复提交。用 e.code 认物理按键，中文输入法、大写锁定下 e.key 不一定是 "s"。
addEventListener("keydown", (e) => {
  if (e.code !== "KeyS" || !(e.ctrlKey || e.metaKey) || e.altKey || e.shiftKey) return
  e.preventDefault()
  saveBtn.click()
})

const revertBtn = document.getElementById("revert")
revertBtn.onclick = () => {
  if (dirty && !confirm("放弃所有未保存的改动，重新读取 data.json？")) return
  dirty = false          // 绕开 beforeunload 拦截
  location.reload()
}

// Esc 等于点「放弃改动」，同样先弹确认，按错了点取消就回来了。
// 三种情况不接：
//   开着对话框   Esc 是关扫描对话框的，<dialog> 自己会处理
//   输入法在拼字 这一下 Esc 是取消拼写，不是冲着页面来的
//   没有改动     放弃也没东西可放弃，别白白重载一遍页面
addEventListener("keydown", (e) => {
  if (e.key !== "Escape" || e.isComposing || !dirty || document.querySelector("dialog[open]")) return
  e.preventDefault()
  revertBtn.click()
})

// —— 网站图标 ——
// 抓取是服务端照着磁盘上的 data.json 去抓的，所以页面上有没保存的改动时按钮不让点，
// 旁边写清楚为什么 —— 不然刚改的链接抓的还是旧地址，看着像抓错了。
// 抓完不动 data.json，页面手里的指纹照样能用。
const iconsBtn = document.getElementById("icons-fetch")
const iconsStatus = document.getElementById("icons-status")
const iconsList = document.getElementById("icons-list")
const siteTpl = document.getElementById("site-tpl").content.querySelector(".site")
let fetching = false
let idle = iconsStatus.textContent.trim()   // 没在抓、也没有未保存改动时显示的那句：进来是「已抓取 x / y」，抓完换成结果

const tellIcons = (text, kind = "") => { iconsStatus.textContent = text; iconsStatus.dataset.kind = kind }

// 按钮能不能点只看两件事：有没有没保存的改动、是不是正在抓。markDirty 和保存成功后都会调一次。
function gateIcons() {
  iconsBtn.disabled = dirty || fetching
  if (dirty && !fetching) tellIcons("有未保存的改动，先保存再抓取（抓的是已保存的书签）")
  else if (!fetching && iconsStatus.textContent.startsWith("有未保存")) tellIcons(idle)
}

// 换掉整张列表。没抓到的原因直接写在那一行下面 —— 放 title 里的话触屏和键盘都看不到。
const renderSites = (sites) => {
  iconsList.replaceChildren(...sites.map((s) => {
    const li = siteTpl.cloneNode(true)
    const img = li.querySelector("img")
    const globe = li.querySelector(".site__globe")
    if (s.src) { img.src = s.src; globe.remove() } else img.remove()
    li.dataset.state = s.error ? "bad" : s.src ? "ok" : "none"
    li.querySelector(".site__name").textContent = s.name
    li.querySelector(".site__url").textContent = s.url
    const st = li.querySelector(".site__state")
    st.textContent = s.error ? (s.src ? "没抓到，沿用上次的" : "没抓到") : s.src ? "已抓取" : "还没抓"
    li.querySelector(".site__why").textContent = s.error ?? ""
    return li
  }))
}

iconsBtn.onclick = async () => {
  if (dirty) return gateIcons()
  fetching = true
  gateIcons()
  iconsBtn.setAttribute("aria-busy", "true")
  iconsBtn.querySelector(".btn__text").textContent = "正在抓取…"
  tellIcons("每个网站最多等 5 秒，连不上的会跳过")
  try {
    const res = await fetch("/api/icons/refresh", { method: "POST" })
    const text = (await res.text()).trim()
    if (!res.ok) return tellIcons(text || `抓取失败（HTTP ${res.status}）`, "bad")
    const { sites } = JSON.parse(text)
    renderSites(sites)
    const bad = sites.filter((s) => s.error).length
    let msg = bad
      ? `${sites.length} 个网站，抓到 ${sites.length - bad} 个，${bad} 个没抓到，原因见下面列表`
      : `${sites.length} 个网站的图标都抓到了`
    if (!document.getElementById("favicons").checked) msg += "。打开上面的开关并保存，导航页才会显示"
    idle = msg
    tellIcons(msg, bad ? "" : "ok")
    new BroadcastChannel("nav").postMessage("saved")   // 开着的导航页刷新一下，换上新图标
  } catch (err) {
    tellIcons("抓取失败：" + err.message, "bad")
  } finally {
    fetching = false
    iconsBtn.removeAttribute("aria-busy")
    iconsBtn.querySelector(".btn__text").textContent = "抓取全部图标"
    gateIcons()
  }
}

// —— 深浅色（和导航页共用 localStorage 里的同一个值）——
document.getElementById("theme")?.addEventListener("click", () => {
  const t = document.documentElement.dataset.theme === "dark" ? "light" : "dark"
  document.documentElement.dataset.theme = t
  try { localStorage.setItem("theme", t) } catch { /* 隐私模式下忽略 */ }
})

// 首屏：选中第一个分组，并把侧边栏的条数算出来
syncNav()
show(sections()[0]?.dataset.group)

// —— 扫描服务 ——
// 给一个地址，把上面开着的服务列出来，勾中的直接变成当前分组里的书签行。
// 这里同样没有数据副本：列表就是结果本身，「加入」时现场读一遍列表的文本。

const dlg = document.getElementById("scan-dlg")
const hitTpl = document.getElementById("scan-tpl").content.querySelector(".hit")
const hits = document.getElementById("scan-list")
const scanStatus = document.getElementById("scan-status")
const scanHost = document.getElementById("scan-host")
const goBtn = document.getElementById("scan-go")

const tell = (text, kind = "") => { scanStatus.textContent = text; scanStatus.dataset.kind = kind }

// 地址框的 placeholder 是服务端填的本机局域网 IP（见 internal/scan 的 LocalIP），
// 比 location.hostname 准：从哪台机器打开这个页面，跟服务跑在哪个网段是两回事。
document.getElementById("scan-open").onclick = () => {
  dlg.showModal()
  scanHost.focus()
}

// 关掉对话框就把还在飞的那轮掐掉：服务端挂在请求 context 上，
// 连接一断，剩下几百个探测立刻收手，不会在后台慢慢超时。
let live = null
document.getElementById("scan-close").onclick = () => dlg.close()
dlg.addEventListener("close", () => live?.abort())

goBtn.onclick = async () => {
  const host = scanHost.value.trim()
  if (!host) return tell(`先填个地址，比如 ${scanHost.placeholder || "192.168.1.10"}`, "bad")

  live?.abort()
  live = new AbortController()
  goBtn.disabled = true
  hits.replaceChildren()
  tell("扫描中…")
  try {
    const res = await fetch("/api/scan", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ host, ports: document.getElementById("scan-ports").value }),
      signal: live.signal,
    })
    const text = (await res.text()).trim()
    if (!res.ok) return tell(text || `扫描失败（HTTP ${res.status}）`, "bad")

    const { scanned, open, node } = JSON.parse(text)
    render(host, open)
    // node 有值：对方也跑着导航页，端口是它自己报的，没扫描（服务端的 internal/peer）
    if (node) return tell(`这是导航页节点「${node}」，直接问它要的端口，没扫描：${scanned} 个端口里开着 ${open.length} 个`, open.length ? "ok" : "")
    tell(open.length
      ? `扫了 ${scanned} 个端口，开着 ${open.length} 个`
      : `扫了 ${scanned} 个端口，一个都没开`, open.length ? "ok" : "")
  } catch (err) {
    if (err.name !== "AbortError") tell("扫描失败：" + err.message, "bad")
  } finally {
    goBtn.disabled = false
  }
}

// 描述栏：Server 头、非 200 的状态码、以及「这根本不是网页」。
// 都没有就留空 —— 宁可空着，也别塞一句「未知服务」进 data.json。
const tag = (h) => [
  h.server,
  h.status && h.status !== 200 ? `HTTP ${h.status}` : "",
  h.http ? "" : "非网页服务",
].filter(Boolean).join("，")

// isSelf 判断这回扫的是不是「跑着这个导航页的那台机器」。是的话书签存成 ":8096"
// 而不是写死一个 IP —— 从哪个地址打开导航页，它就跟到哪个地址上去（见 README
// 「跟着当前地址走的书签」）。
//
// 算本机的有三类，都确确实实是这台机器，不会误伤别人家的地址：
//   location.hostname  你此刻用来打开这个页面的地址，它必然通到这个服务
//   placeholder        服务端自己算出来的局域网 IP（认不出来是空串，匹配不上）
//   回环那几个         扫描是服务端发起的，127.0.0.1 说的就是服务端自己
const isSelf = (host) =>
  [location.hostname, scanHost.placeholder, "localhost", "127.0.0.1", "::1"].includes(host)

const render = (host, open) => {
  const self = isSelf(host)
  hits.toggleAttribute("data-self", self)
  document.getElementById("scan-rel").hidden = !self
  // 已经收进书签的就不用再加一遍，默认不勾；本机的还要比一遍 ":端口" 那种写法。
  const have = new Set([...main.querySelectorAll(".f-url")].map((i) => i.value.trim().replace(/\/$/, "")))
  hits.replaceChildren(...open.map((h) => {
    const li = hitTpl.cloneNode(true)
    li.querySelector(".hit__port").textContent = h.port
    // 连标题都没有时退回拿地址当名字 —— 本机的话这里也要用 ":8098"，
    // 不然链接是跟着地址走的、名字却写死一个 IP，换台机器看就对不上了。
    li.querySelector(".hit__name").textContent =
      h.title || h.hint || (self ? `:${h.port}` : `${host}:${h.port}`)
    li.querySelector(".hit__tag").textContent = tag(h)
    const a = li.querySelector(".hit__url")
    a.href = h.url
    a.textContent = h.url        // href 会被补成带斜杠的绝对地址，写进书签的要原样这一份
    // 不是网页的（SSH、数据库这些）默认不勾：导航页放的是能点开的链接。
    li.querySelector(".hit__on").checked =
      h.http && !have.has(h.url) && !(self && have.has(":" + h.port))
    return li
  }))
}

document.getElementById("scan-add").onclick = () => {
  const picked = [...hits.querySelectorAll(".hit")].filter((li) => li.querySelector(".hit__on").checked)
  if (!picked.length) return tell("先勾上几条", "bad")

  const sec = sections().find((s) => s.classList.contains("is-active"))
  if (!sec) return tell("先关掉对话框，在左边选一个要加进去的分组", "bad")   // 当前开着的是设置面板
  // 扫的是本机就存 ":8096"。只对 http 这么干 —— ":端口" 补出来的是 http，
  // 把一个 https 服务写成这样等于把协议弄丢了，那种老老实实存绝对地址。
  const rel = hits.hasAttribute("data-self")
  for (const li of picked) {
    const row = rowTpl.cloneNode(true)
    const url = li.querySelector(".hit__url").textContent
    row.querySelector(".f-name").value = li.querySelector(".hit__name").textContent
    row.querySelector(".f-url").value =
      rel && url.startsWith("http://") ? ":" + li.querySelector(".hit__port").textContent : url
    row.querySelector(".f-desc").value = li.querySelector(".hit__tag").textContent
    sec.querySelector(".rows").append(row)
  }
  syncNav()
  markDirty()
  dlg.close()
  say(`已加入 ${picked.length} 条到「${sec.querySelector(".f-gname").value.trim()}」` + (rel ? "，本机服务存成了 :端口" : "") + "，还没保存")
}
