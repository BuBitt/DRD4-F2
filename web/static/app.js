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

        // SS2 bulk-download handlers
        const downloadAllBtn = document.getElementById('psipred-download-ss2-all')
        if (downloadAllBtn) {
            downloadAllBtn.addEventListener('click', async function (ev) {
                ev.preventDefault()
                if (!confirm('Iniciar download de todos os arquivos .ss2 para variantes com resultados completos?')) return
                // open modal
                const modal = document.getElementById('psipred-ss2-modal')
                const bar = document.getElementById('psipred-ss2-bar')
                const summary = document.getElementById('psipred-ss2-summary')
                const list = document.getElementById('psipred-ss2-list')
                if (!modal || !bar || !summary || !list) return
                modal.setAttribute('aria-hidden', 'false')
                summary.textContent = 'Iniciando...'
                list.innerHTML = ''
                bar.style.width = '0%'

                // start on server
                try {
                    const res = await fetch('/api/psipred/download-ss2/all', { method: 'POST' })
                    if (!res.ok) {
                        const txt = await res.text()
                        alert('Falha ao iniciar: ' + txt)
                        modal.setAttribute('aria-hidden', 'true')
                        return
                    }
                } catch (e) {
                    alert('Falha ao contactar servidor: ' + e.message)
                    modal.setAttribute('aria-hidden', 'true')
                    return
                }

                // poll status every 2s
                let stopped = false
                const closeBtn = document.getElementById('psipred-ss2-close')
                if (closeBtn) closeBtn.addEventListener('click', function () { modal.setAttribute('aria-hidden', 'true'); stopped = true })

                async function pollStatus() {
                    if (stopped) return
                    try {
                        const r = await fetch('/api/psipred/download-ss2/status')
                        if (!r.ok) { summary.textContent = 'Erro ao consultar status'; return }
                        const s = await r.json()
                        const total = s.total || 0
                        const done = s.done || 0
                        const running = s.running === true
                        const current = s.current || ''
                        const per = s.per_job || {}
                        const pct = total === 0 ? 100 : Math.round((done / total) * 100)
                        bar.style.width = pct + '%'
                        summary.textContent = `Progresso: ${done}/${total} (${pct}%) ${running ? '— em execução: ' + current : ''}`
                        // populate list
                        list.innerHTML = ''
                        Object.keys(per).forEach(k => {
                            const li = document.createElement('li')
                            li.textContent = `${k} — ${per[k]}`
                            list.appendChild(li)
                        })
                        if (running) {
                            setTimeout(pollStatus, 2000)
                        } else {
                            // finished
                            setTimeout(() => { modal.setAttribute('aria-hidden', 'true') }, 800)
                            // refresh jobs list after short delay
                            setTimeout(refreshJobsList, 1000)
                        }
                    } catch (e) {
                        summary.textContent = 'Erro: ' + e.message
                    }
                }
                setTimeout(pollStatus, 500)
            })
        }

        // submit-all button (PSIPRED Jobs page)
        const submitAll = document.getElementById('psipred-submit-all')
        if (submitAll) {
            submitAll.addEventListener('click', async function (ev) {
                ev.preventDefault()
                if (!confirm('Enviar todas as variantes que ainda não foram enviadas para o PSIPRED?')) return
                submitAll.disabled = true
                const origText = submitAll.textContent
                submitAll.textContent = 'Enviando...'
                try {
                    let res = await fetch('/psipred/submit-all', { method: 'POST' })
                    if (res.status === 401) {
                        const token = prompt('Endpoint protegido. Insira o token de submissão:')
                        if (!token) throw new Error('token não fornecido')
                        res = await fetch('/psipred/submit-all', { method: 'POST', headers: { 'X-Submit-All-Token': token } })
                    }
                    if (!res.ok) throw new Error(await res.text())
                    const data = await res.json()
                    // show created count
                    const count = data.created ? data.created.length : 0
                    alert('Submetidos: ' + count + ' variantes')
                    // refresh jobs and pollers views
                    setTimeout(() => { refreshJobsList(); }, 1000)
                } catch (e) { alert('Falha: ' + e.message) }
                submitAll.disabled = false
                submitAll.textContent = origText
            })
        }
    }

    // Wire filter form clear button and pre-fill inputs from URL
    function wireFilterForm() {
        const form = document.getElementById('psipred-jobs-filter')
        if (!form) return
        const q = new URLSearchParams(window.location.search)
        const qv = q.get('q') || ''
        const sv = q.get('state') || ''
        const inQ = document.getElementById('psipred-filter-q')
        const inS = document.getElementById('psipred-filter-state')
        if (inQ) inQ.value = qv
        if (inS) inS.value = sv

        const clear = document.getElementById('psipred-clear-filter')
        if (clear) {
            clear.addEventListener('click', function (ev) {
                ev.preventDefault()
                if (inQ) inQ.value = ''
                if (inS) inS.value = ''
                // submit the form to reload with no filters
                form.submit()
            })
        }
    }

    // (Removed filter interception) Let the form submit natively so server-side filtering is used.

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
        // legacy direct-submit forms (if any)
        document.querySelectorAll('form[action^="/psipred/submit/"]').forEach(f => {
            f.addEventListener('submit', async function (ev) {
                ev.preventDefault()
                // fallback: behave like preview confirm
                const action = f.getAttribute('action')
                const code = action.replace('/psipred/submit/', '')
                await openFastaPreviewAndSubmit(code)
            })
        })
        // preview-send button in detail template
        const previewBtn = document.getElementById('preview-send')
        if (previewBtn) {
            previewBtn.addEventListener('click', async function (ev) {
                ev.preventDefault()
                const code = this.getAttribute('data-variant')
                await openFastaPreviewAndSubmit(code)
            })
        }
    }

    // client-side cleaning function: keep only standard amino-acids
    function clientCleanSequence(s) {
        const allowed = new Set('ACDEFGHIKLMNPQRSTVWY')
        let lines = s.split('\n')
        let raw = ''
        for (let line of lines) {
            line = line.trim()
            if (!line) continue
            if (line.startsWith('>')) continue
            raw += line
        }
        let out = ''
        for (let i = 0; i < raw.length; i++) {
            let ch = raw[i]
            if (ch >= 'a' && ch <= 'z') ch = ch.toUpperCase()
            if (allowed.has(ch)) out += ch
        }
        return out
    }

    async function openFastaPreviewAndSubmit(variantCode) {
        // read the TranslateMergedRef from the DOM
        const pre = document.getElementById('translate-merged-ref')
        const text = pre ? pre.textContent : ''
        const clean = clientCleanSequence(text)
        // show only the sequence in the preview (no header), server will send only the sequence as well
        const fasta = `${clean}\n`
        const modal = document.getElementById('fasta-preview-modal')
        const body = document.getElementById('fasta-preview-body')
        if (modal) modal.setAttribute('aria-hidden', 'false')
        if (body) body.textContent = fasta
        // wire confirm/cancel handlers
        const close = document.getElementById('fasta-preview-close')
        const cancel = document.getElementById('fasta-send-cancel')
        const confirmBtn = document.getElementById('fasta-send-confirm')
        const hide = () => { modal && modal.setAttribute('aria-hidden', 'true') }
        const oneTime = async (ev) => {
            if (ev && ev.preventDefault) ev.preventDefault()
            if (confirmBtn) confirmBtn.disabled = true
            // perform POST to server submit endpoint
            try {
                const res = await fetch('/psipred/submit/' + variantCode, { method: 'POST' })
                if (!res.ok) throw new Error(await res.text())
                const data = await res.json()
                hide()
                // if the submit response already includes a remote_uuid, mark the variant as sent
                try {
                    const codeToMark = data && (data.variant_code || variantCode)
                    if (data && data.remote_uuid && codeToMark) markVariantSent(codeToMark)
                } catch (e) { /* ignore */ }
                if (data && data.job_id) pollInternalJob(data.job_id)
            } catch (e) {
                alert('Falha ao submeter: ' + e.message)
            } finally {
                // cleanup handlers and re-enable button
                if (confirmBtn) confirmBtn.disabled = false
                try { if (confirmBtn) confirmBtn.removeEventListener('click', oneTime) } catch (e) { }
                try { if (close) close.removeEventListener('click', hide) } catch (e) { }
                try { if (cancel) cancel.removeEventListener('click', hide) } catch (e) { }
            }
        }
        // attach handlers (use once option for confirm to be safe)
        if (confirmBtn) confirmBtn.addEventListener('click', oneTime, { once: true })
        if (close) close.addEventListener('click', hide)
        if (cancel) cancel.addEventListener('click', hide)
    }

    async function pollJob(uuid) {
        try {
            const el = document.getElementById('psipred-status')
            el && (el.textContent = 'Consultando...')
            const res = await fetch('/psipred/status/' + uuid)
            if (!res.ok) { el && (el.textContent = 'Erro ao consultar'); return }
            const data = await res.json()
            el && (el.textContent = JSON.stringify(data))
            // if job has remote_uuid, mark variant as sent in the menu
            if (data.remote_uuid && data.variant_code) {
                markVariantSent(data.variant_code)
            }
        } catch (e) { console.error(e) }
    }

    // mark a variant in the left menu as sent (adds .sent class and a visible badge)
    function markVariantSent(variantCode) {
        if (!variantCode) return
        try {
            const selector = 'a.item[href="/variant/' + variantCode + '"]'
            const a = document.querySelector(selector)
            if (!a) return
            if (!a.classList.contains('sent')) a.classList.add('sent')
            // ensure sent-badge exists near the code element
            if (!a.querySelector('.sent-badge')) {
                const codeEl = a.querySelector('.code')
                const badge = document.createElement('span')
                badge.className = 'sent-badge'
                badge.textContent = 'Enviado'
                if (codeEl && codeEl.parentNode) {
                    codeEl.parentNode.insertBefore(badge, codeEl.nextSibling)
                } else {
                    a.appendChild(badge)
                }
            }
        } catch (e) {
            // ignore
        }
    }

    async function pollInternalJob(id) {
        try {
            const el = document.getElementById('psipred-status')
            el && (el.textContent = 'Acompanhando job interno...')
            const res = await fetch('/api/psipred/job/' + id)
            if (!res.ok) {
                // If job not yet persisted the server may return 404 briefly; retry a few times.
                if (res.status === 404) {
                    el && (el.textContent = 'Aguardando criação do job...')
                    setTimeout(() => pollInternalJob(id), 2000)
                    return
                }
                el && (el.textContent = 'Erro ao consultar job')
                return
            }
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
                const status = j.remote_state && j.remote_state !== '' ? j.remote_state : j.state
                tr.innerHTML = `<td><code>${j.id}</code></td><td><a href="/variant/${j.variant_code}">${j.variant_code}</a></td><td>${j.remote_uuid ? `<a href="/psipred/job/${j.remote_uuid}">${j.remote_uuid}</a>` : '—'}</td><td><span class="badge">${status}</span></td><td><a class="btn secondary" href="/psipred/job/${j.remote_uuid || j.id}">Ver</a> <a class="btn" href="#" data-retry="${j.id}">Retry</a></td>`
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
            // fetch failures and show
            try {
                const fres = await fetch('/api/psipred/failures')
                if (fres.ok) {
                    const fails = await fres.json()
                    if (fails && fails.length) {
                        const items = fails.map(f => ({ variant: f.variant_code || f.variantCode || f.VariantCode, job_id: f.id || f.ID, message: f.message }))
                        showFailures(items)
                    }
                }
            } catch (e) { /* ignore */ }
        } catch (e) { console.error(e) }
    }

    // Progress UI helpers
    function openSubmitAllProgress(created) {
        const modal = document.getElementById('psipred-progress-modal')
        const close = document.getElementById('psipred-progress-close')
        const summary = document.getElementById('psipred-progress-summary')
        const bar = document.getElementById('psipred-progress-bar')
        const list = document.getElementById('psipred-progress-list')
        if (!modal || !bar || !list || !summary) return
        modal.setAttribute('aria-hidden', 'false')
        summary.textContent = `Submetendo ${created.length} variantes...`
        list.innerHTML = ''
        const total = created.length
        let completed = 0
        const failures = []

        function updateBar() {
            const pct = total === 0 ? 100 : Math.round((completed / total) * 100)
            bar.style.width = pct + '%'
            summary.textContent = `Progresso: ${completed}/${total} (${pct}%)`
        }

        // create list entries and start polling for each job id
        created.forEach(item => {
            const variant = item.variant || item[0] || ''
            const jobID = item.job_id || item[1] || ''
            const li = document.createElement('li')
            li.textContent = `${variant} — aguardando criação do job...`
            list.appendChild(li)

                // poll internal job until final and update li
                (async function poll(jobID, li, variant) {
                    // wait for job to appear and then poll state
                    let attempts = 0
                    let final = false
                    while (!final && attempts < 300) { // cap attempts to avoid infinite loop (~40 mins if interval 8s)
                        try {
                            const res = await fetch('/api/psipred/job/' + jobID)
                            if (res.status === 404) {
                                li.textContent = `${variant} — aguardando criação... (${attempts})`
                            } else if (!res.ok) {
                                li.textContent = `${variant} — erro (${res.status})`
                            } else {
                                const j = await res.json()
                                const state = j.state || j.remote_state || 'unknown'
                                li.textContent = `${variant} — ${state}`
                                if (state === 'complete' || state === 'error') {
                                    final = true
                                    if (state === 'error') {
                                        failures.push({ variant: variant, job_id: jobID, message: j.message || 'remote error' })
                                    }
                                    // if remote uuid present, mark the variant as sent
                                    if (j.remote_uuid) markVariantSent(j.variant_code || variant)
                                    completed++
                                    updateBar()
                                    break
                                }
                            }
                        } catch (e) {
                            li.textContent = `${variant} — falha: ${e.message}`
                        }
                        attempts++
                        await new Promise(r => setTimeout(r, 8000))
                    }
                    if (!final) {
                        // consider timed out as failure
                        failures.push({ variant: variant, job_id: jobID, message: 'timeout' })
                        completed++
                        updateBar()
                    }
                    // when all done, close and surface failures
                    if (completed >= total) {
                        setTimeout(() => {
                            modal.setAttribute('aria-hidden', 'true')
                            if (failures.length > 0) showFailures(failures)
                            refreshJobsList()
                        }, 800)
                    }
                })(jobID, li, variant)
        })
        updateBar()
        if (close) close.addEventListener('click', function () { modal.setAttribute('aria-hidden', 'true') })
    }

    // Display failures in the failures section
    function showFailures(failures) {
        const sec = document.getElementById('psipred-failures')
        const list = document.getElementById('psipred-failures-list')
        if (!sec || !list) return
        list.innerHTML = ''
        failures.forEach(f => {
            const li = document.createElement('li')
            li.innerHTML = `<strong>${f.variant}</strong> — ${f.message} ${f.job_id ? `(<code>${f.job_id}</code>)` : ''} <a href="#" data-retry-variant="${f.variant}">Reenviar</a>`
            list.appendChild(li)
        })
        sec.style.display = failures.length ? 'block' : 'none'

        // wire reenviar handlers
        document.querySelectorAll('a[data-retry-variant]').forEach(a => {
            a.addEventListener('click', async function (ev) {
                ev.preventDefault()
                const variant = this.getAttribute('data-retry-variant')
                try {
                    const resp = await fetch('/psipred/submit/' + variant, { method: 'POST' })
                    if (!resp.ok) throw new Error(await resp.text())
                    // optimistic: remove item from list
                    this.parentElement.remove()
                } catch (e) {
                    alert('Falha ao reenviar: ' + e.message)
                }
            })
        })
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
    wireFilterForm()
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
