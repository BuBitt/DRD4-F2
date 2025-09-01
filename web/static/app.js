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
        // attach view/status buttons on jobs table if present
        document.querySelectorAll('button[data-job-id]').forEach(btn => {
            btn.addEventListener('click', async function (ev) {
                ev.preventDefault()
                const id = this.getAttribute('data-job-id')
                openJobModal(id)
            })
        })
        document.querySelectorAll('a.view-link').forEach(a => {
            a.addEventListener('click', function (ev) {
                // allow normal navigation but also open modal if same-origin
                ev.preventDefault()
                const href = a.getAttribute('href')
                // if it's a remote uuid link, open modal with the id
                const last = href.split('/').pop()
                openJobModal(last)
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
                    // server now returns { job_id: "..." }
                    const data = await fetchJSON(action, { method: 'POST' })
                    btn.textContent = 'Enviado'
                    if (data && data.job_id) {
                        // poll internal API for job state
                        pollInternalJob(data.job_id)
                    }
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

    async function pollInternalJob(id) {
        try {
            const el = document.getElementById('psipred-status')
            el && (el.textContent = 'Acompanhando job interno...')
            const res = await fetch('/api/psipred/job/' + id)
            if (!res.ok) { el && (el.textContent = 'Erro ao consultar job'); return }
            const data = await res.json()
            el && (el.textContent = JSON.stringify(data))
            // if not final, poll again in 8s
            if (data.state && (data.state === 'queued' || data.state === 'submitting' || data.state === 'submitted' || data.state === 'polling')) {
                setTimeout(() => pollInternalJob(id), 8000)
            }
        } catch (e) { console.error(e) }
    }

    // Modal helpers
    function openJobModal(id) {
        const modal = document.getElementById('job-modal')
        const pre = document.getElementById('job-modal-pre')
        modal && modal.setAttribute('aria-hidden', 'false')
        pre && (pre.textContent = 'Carregando...')
        fetch('/api/psipred/job/' + id).then(async res => {
            if (!res.ok) throw new Error('job not found')
            const j = await res.json()
            pre.textContent = JSON.stringify(j, null, 2)
            // if job has remote uuid, fetch remote status as well
            if (j.remote_uuid) {
                try {
                    const r = await fetch('/psipred/status/' + j.remote_uuid)
                    if (r.ok) {
                        const txt = await r.text()
                        pre.textContent += '\n\n--- remote status ---\n' + txt
                    }
                } catch (e) { /* ignore */ }
            }
            // start polling internal job until final
            if (j.state && (j.state === 'queued' || j.state === 'submitting' || j.state === 'submitted' || j.state === 'polling')) {
                setTimeout(() => pollAndRefreshModal(id), 4000)
            }
        }).catch(err => { pre && (pre.textContent = 'Erro: ' + err.message) })
    }

    function closeJobModal() {
        const modal = document.getElementById('job-modal')
        if (!modal) return
        modal.setAttribute('aria-hidden', 'true')
    }

    async function pollAndRefreshModal(id) {
        try {
            const res = await fetch('/api/psipred/job/' + id)
            if (!res.ok) return
            const j = await res.json()
            const pre = document.getElementById('job-modal-pre')
            if (pre) pre.textContent = JSON.stringify(j, null, 2)
            if (j.state && (j.state === 'queued' || j.state === 'submitting' || j.state === 'submitted' || j.state === 'polling')) {
                setTimeout(() => pollAndRefreshModal(id), 8000)
            }
        } catch (e) { /* swallow */ }
    }

    // auto-refresh jobs list on the jobs page
    async function refreshJobsList() {
        try {
            const el = document.querySelector('table.jobs tbody')
            if (!el) return
            const res = await fetch('/api/psipred/jobs/list')
            if (!res.ok) return
            const data = await res.json()
            // rebuild rows
            el.innerHTML = ''
            data.forEach(j => {
                const tr = document.createElement('tr')
                tr.innerHTML = `<td><code>${j.id}</code></td><td><a href="/variant/${j.variant_code}">${j.variant_code}</a></td><td>${j.remote_uuid ? `<a href="/psipred/job/${j.remote_uuid}">${j.remote_uuid}</a>` : '—'}</td><td><span class="badge">${j.state}</span></td><td><a class="btn secondary" href="/psipred/job/${j.remote_uuid || j.id}">Ver</a> <a class="btn" href="#" data-retry="${j.id}">Retry</a></td>`
                el.appendChild(tr)
            })
            // wire retry buttons
            document.querySelectorAll('a[data-retry]').forEach(a => {
                a.addEventListener('click', async function (ev) {
                    ev.preventDefault()
                    const id = this.getAttribute('data-retry')
                    // call internal API to get variant and trigger a re-submit
                    try {
                        const res = await fetch('/api/psipred/job/' + id)
                        if (!res.ok) throw new Error('job not found')
                        const job = await res.json()
                        // call submit endpoint for the variant
                        const resp = await fetch('/psipred/submit/' + job.variant_code, { method: 'POST' })
                        if (!resp.ok) throw new Error('submit failed')
                        alert('Retry triggered')
                    } catch (e) { alert('retry failed: ' + e.message) }
                })
            })
        } catch (e) { console.error(e) }
    }

    // if we are on the jobs page, refresh periodically
    setInterval(refreshJobsList, 10000)

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
    // modal close handlers
    document.getElementById('modal-close') && document.getElementById('modal-close').addEventListener('click', closeJobModal)
    document.querySelectorAll('.modal-backdrop').forEach(b => b.addEventListener('click', closeJobModal))
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
