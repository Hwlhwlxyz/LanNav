// 全部客户端行为都在这里：分组定位、搜索、快捷键、深浅色、时钟。没有框架，没有构建。
// 深浅色的「初始值」不在这里 —— 它必须在首次绘制前跑完，见 index.html 的 <head>。

const search = document.getElementById("search")
const empty = document.querySelector(".empty")
const groups = [...document.querySelectorAll(".group")]
const links = [...document.querySelectorAll(".side__link")]
const nav = document.querySelector(".side nav")

// —— 分组定位 ——
// 所有分组从上到下排在同一个页面里。点侧边栏是滚到那一组，滚动时侧边栏跟着亮。
// 服务端的 /dev 路由依然保留：直接输入地址、刷新、禁用 JS 时都是整页从头显示，
// JS 生效后再按地址栏滚到对应分组。
const fallback = groups[0]?.dataset.group
const fromURL = () => location.pathname.slice(1) || fallback
const topH = parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--top-h")) || 49

let current = fromURL()
// 刚点过侧边栏、或刚按地址栏落位时，高亮以点的那组为准，锁住不让滚动联动改它：
// 一是滚动动画中途经过的分组不该闪一下；二是页面不够长时点的那组可能滚不到顶，
// 这时按「滚到底算最后一组」的规则会把高亮挪走，用户刚点的却不亮，不对。
// 用户自己动手滚（滚轮、触摸、拖滚动条、键盘）才解锁。
let lock = false

const mark = (id) => {
  current = id
  for (const a of links) {
    if (a.dataset.group === id) a.setAttribute("aria-current", "page")
    else a.removeAttribute("aria-current")
  }
  // 窄屏下侧边栏是横向条：当前项换了就把它滚进可视范围。只滚这个条，不碰页面。
  const a = links.find((l) => l.dataset.group === id)
  if (a && nav.scrollWidth > nav.clientWidth)
    nav.scrollTo({ left: a.offsetLeft - (nav.clientWidth - a.offsetWidth) / 2, behavior: "smooth" })
}

const jump = (id, behavior = "smooth") => {
  const s = groups.find((g) => g.dataset.group === id)
  if (!s) return
  mark(id)
  lock = true
  s.scrollIntoView({ behavior, block: "start" })
}
// 滚动动画自己发出的 scroll 事件不算「用户在滚」，所以不在 scroll 或 scrollend 上解锁
for (const t of ["wheel", "touchstart", "pointerdown", "keydown"])
  addEventListener(t, () => { lock = false }, { passive: true })

nav.onclick = (e) => {
  const a = e.target.closest(".side__link")
  // 修饰键交给浏览器：ctrl/cmd 点击照常在新标签页打开
  if (!a || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey) return
  e.preventDefault()
  if (search.value) { search.value = ""; search.oninput() }
  jump(a.dataset.group)
  history.pushState(null, "", a.getAttribute("href"))
}

// 滚动时看哪个分组是「当前」：贴顶标题还完整露在顶栏下面的第一个分组。
// 判断用的是分组的底边而不是顶边：上一组只剩被顶栏挡住的那截时，它的贴顶标题已经开始被顶出去，
// 用户眼里看到的标题是下一组的，侧边栏要和看到的那个标题一致，不能还亮着上一组。
// 页面已经滚到底时直接算最后一组 —— 不然最后一组太短永远轮不到它，侧边栏会卡在倒数第二个。
// 搜索收起的分组（display:none）不参与。改的是 replaceState 不是 pushState：滚一屏不该多一条历史。
const sticky = (g) => g.querySelector(".svc__head, .group__title")
const spy = () => {
  if (lock) return
  const vis = groups.filter((g) => g.offsetParent !== null)
  if (!vis.length) return
  const bottom = scrollY + innerHeight >= document.documentElement.scrollHeight - 2
  const cur = bottom
    ? vis.at(-1)
    : vis.find((g) => g.getBoundingClientRect().bottom > topH + sticky(g).offsetHeight) ?? vis.at(-1)
  if (cur.dataset.group !== current) {
    mark(cur.dataset.group)
    history.replaceState(null, "", "/" + cur.dataset.group)
  }
}
// scroll 事件本来就是每帧最多一次，不用再套 requestAnimationFrame；
// 套了反而在标签页不可见时（rAF 暂停）把队列卡死，切回来第一下滚动不更新。
addEventListener("scroll", spy, { passive: true })

// 位置由我们管：刷新、前进后退都按地址栏里的分组落位，不用浏览器记的像素位置
history.scrollRestoration = "manual"
addEventListener("popstate", () => jump(fromURL()))
if (location.pathname !== "/") jump(current, "instant")
else mark(current)

// —— 搜索 ——
// 名称和描述本来就渲染在卡片里，所以直接读 textContent，不需要任何预计算属性。
// 再加上 <a> 自带的 hostname，输入 github / 代码托管 / mdn 都能命中。
// 搜索时 body 上挂 data-searching，CSS 据此把没有命中项的分组收起（:has()），清空后全部放回来。
search.oninput = () => {
  const q = search.value.trim().toLowerCase()
  document.body.toggleAttribute("data-searching", q !== "")
  // 第一条命中项挂 data-first：CSS 给它描边，回车打开的也是它，看到的和按到的是同一张。
  let first = null
  for (const a of document.querySelectorAll("a.card")) {
    a.hidden = q !== "" && !(a.hostname + a.textContent).toLowerCase().includes(q)
    a.toggleAttribute("data-first", q !== "" && !a.hidden && !first)
    if (!a.hidden) first ??= a
  }
  // 空状态由 JS 写文案：role="status" 只有在文本真正变化时才会被读屏播报，
  // 写空字符串则命中 CSS 的 .empty:empty 自动收起。
  empty.textContent = q && !first ? `没有匹配「${search.value.trim()}」的书签，换个关键词试试` : ""
  // 清空搜索时收起的分组会重新展开、页面重排，但不会触发 scroll 事件，侧边栏高亮要自己重算一次
  if (!q) spy()
}

// —— 键盘 ——
// 打开页面就能直接打字：焦点不在输入框里时，敲任何一个可打印字符都会聚焦搜索框，
// 这一下按键不拦，由浏览器照常输进框里，所以第一个字母不会丢。
// / 单独处理：它是「聚焦」而不是「输入一个斜杠」。空格放过，留给翻页。
// 带 Ctrl/Alt/Meta 的组合键是浏览器快捷键，不碰。
// 输入法：中文输入法只在可编辑元素里起作用，焦点在页面上时敲的第一个字母会原样进框，
// 后面的才进入拼音。为了让中文用户一开始就能打，桌面端页面加载时顺手把焦点放进搜索框（见下面）。
addEventListener("keydown", (e) => {
  if (e.target.matches("input, textarea, select") || e.ctrlKey || e.metaKey || e.altKey || e.isComposing) return
  if (e.key === "/") { e.preventDefault(); search.focus(); return }
  if (e.key.length === 1 && e.key !== " ") search.focus()
})

// 桌面上一进来焦点就在搜索框，输入法从第一个字母起就是拼音。
// 窄屏（手机）不这么做：自动聚焦会把软键盘弹出来，盖掉半屏卡片。
// 按宽度判断而不是 (pointer: fine)：带触摸屏的 Windows 笔记本上那个查询是 false，会把鼠标用户也漏掉。
if (matchMedia("(min-width: 721px)").matches) search.focus({ preventScroll: true })

search.onkeydown = (e) => {
  if (e.key === "Escape" && search.value) { search.value = ""; search.oninput() }
  if (e.key === "Enter" && search.value.trim()) document.querySelector("a.card[data-first]")?.click()
}

// —— 深浅色（只管切换，初始值见 index.html 的 <head>）——
document.getElementById("theme").onclick = () => {
  const t = document.documentElement.dataset.theme === "dark" ? "light" : "dark"
  document.documentElement.dataset.theme = t
  try { localStorage.setItem("theme", t) } catch { /* 隐私模式下忽略 */ }
}

// —— 时钟 ——
// 只显示到分钟，所以不用每秒刷：每次对齐到下一个整分再改一次。
// 不是 live region：读屏到达时读一次当前时间就够，不会被变化打断。
const clock = document.getElementById("clock")
const tick = () => {
  clock.textContent = new Date().toLocaleTimeString("zh-CN", { hour12: false, hour: "2-digit", minute: "2-digit" })
  setTimeout(tick, 60000 - Date.now() % 60000)
}
tick()

// —— 跟着编辑页走 ——
// /admin 保存成功后会广播一声，这里收到就重新拉一遍页面。
// 只在同一个浏览器内有效（广播走浏览器进程内部，不过网络）。
new BroadcastChannel("nav").onmessage = () => location.reload()

// —— 发现的服务 ——
// 结果缓存在服务端内存里，不定时刷新。点「刷新」才让服务端按编辑页里的设置重扫一轮，
// 扫完整页重载 —— 卡片是服务端渲染的，重载一遍比在这里拼 DOM 省事，搜索也自然照常。
// 地址栏这时已经是 /services（点侧边栏时 pushState 过），重载后还停在这一栏。
const svc = document.querySelector(".svc")
if (svc) {
  const btn = svc.querySelector(".svc__refresh")
  const meta = svc.querySelector(".svc__meta")

  const refresh = async () => {
    btn.disabled = true
    meta.textContent = "正在扫描…（按设置的地址和端口数，可能要几十秒）"
    try {
      // 服务端已经在扫就不会再开一轮，这个请求会等那一轮扫完再回来
      const res = await fetch("/api/services/refresh", { method: "POST" })
      if (res.ok) return location.reload()
      meta.textContent = (await res.text()).trim() || `扫描失败（HTTP ${res.status}）`
    } catch (err) {
      meta.textContent = "扫描失败：" + err.message
    }
    btn.disabled = false
  }

  btn.onclick = refresh
  // 打开页面时别人刚好点了刷新、还在扫：接上那一轮，扫完自己重载
  if (svc.hasAttribute("data-running")) refresh()
}
