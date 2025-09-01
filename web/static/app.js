// Simple client app to power interactions (fetch variants, show details, submit PSIPRED, poll)
(async function () {
    const $ = sel => document.querySelector(sel)
    const $$ = sel => Array.from(document.querySelectorAll(sel))

    async function fetchJSON(url, opts) {
        const res = await fetch(url, opts)
        if (!res.ok) throw new Error(await res.text())
        return res.json()
    }

    // load variants list (server already rendered list but we can refresh)
    async function loadVariants(q = '') {
        try {
            const url = '/variants?q=' + encodeURIComponent(q)
            const res = await fetch(url)
            const html = await res.text()
            const container = document.getElementById('variants')
            if (container) container.innerHTML = html
            attachListHandlers()
        } catch (e) { console.error(e) }
    }

    function attachListHandlers() {
        $$('.item').forEach(a => {
            a.addEventListener('click', async function (ev) {
                ev.preventDefault()
                const href = a.getAttribute('href')
                history.pushState(null, '', href)
                await loadVariantDetail(href)
            })
        })
    }

    async function loadVariantDetail(path) {
        try {
            const code = path.replace('/variant/', '')
            const res = await fetch('/variant/' + code, { headers: { 'X-Requested-With': 'XMLHttpRequest' } })
            const html = await res.text()
            const detail = document.getElementById('detail')
            if (detail) detail.innerHTML = html
            attachDetailHandlers()
        } catch (e) { console.error(e) }
    }

    function attachDetailHandlers() {
        document.querySelectorAll('form[action^="/psipred/submit/"]').forEach(f => {
            f.addEventListener('submit', async function (ev) {
                ev.preventDefault()
                const action = f.getAttribute('action')
                const btn = f.querySelector('button')
                const old = btn.textContent
                btn.disabled = true; btn.textContent = 'Enviando...'
                try {
                    const data = await fetchJSON(action, { method: 'POST' })
                    btn.textContent = 'Enviado'
                    pollJob(data.uuid)
                } catch (err) {
                    btn.textContent = 'Erro'
                    console.error(err)
                } finally { setTimeout(() => { btn.disabled = false; btn.textContent = old }, 1500) }
            })
        })
    }

    async function pollJob(uuid) {
        try {
            const el = document.getElementById('psipred-status')
            el && (el.textContent = 'Consultando...')
            const res = await fetch('/psipred/status/' + uuid)
            if (!res.ok) { el && (el.textContent = 'Erro ao consultar'); return }
            const data = await res.json()
            el && (el.textContent = JSON.stringify(data))
        } catch (e) { console.error(e) }
    }

    // search form removed from UI; no client-side search wiring

    // handle back/forward
    window.addEventListener('popstate', function (ev) { loadCurrentFromLocation() })
    async function loadCurrentFromLocation() {
        const p = window.location.pathname
        if (p.startsWith('/variant/')) await loadVariantDetail(p)
    }

    // initial wire
    attachListHandlers()
    attachDetailHandlers()
    // load variant if url points to one
    loadCurrentFromLocation()
})()

    /* Role-aware UI constraints: read role from body and enforce max width for detail area
       This is lightweight: it doesn't change server behavior, only keeps the detail panel within
       the CSS --max-expand value and provides an optional visual cue when expansion is limited. */
    (function () {
        function clampDetail() {
            const body = document.body
            const role = body && body.getAttribute('data-role') || 'guest'
            const detail = document.getElementById('detail')
            if (!detail) return
            // read computed max width from CSS variable if present
            const root = getComputedStyle(detail.parentElement || document.documentElement)
            const max = root.getPropertyValue('--max-expand') || ''
            if (max) {
                detail.style.maxWidth = max.trim()
            }
            // add a small badge for non-admins when content is large
            if (role !== 'admin') {
                const badgeId = 'expansion-note'
                let b = document.getElementById(badgeId)
                if (!b) {
                    b = document.createElement('div')
                    b.id = badgeId
                    b.style.fontSize = '12px'
                    b.style.color = 'var(--muted)'
                    b.style.marginTop = '6px'
                        (detail.parentElement || detail).appendChild(b)
                }
                b.textContent = role === 'guest' ? 'Limite de visualização aplicado para visitantes.' : 'Limite de visualização aplicado.'
            }
        }
        // run on load and whenever the detail area updates
        window.addEventListener('load', clampDetail)
        const observer = new MutationObserver(clampDetail)
        observer.observe(document.getElementById('detail') || document.body, { childList: true, subtree: true })
    })()
