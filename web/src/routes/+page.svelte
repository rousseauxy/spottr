<script>
  import { onMount, tick } from 'svelte';
  import { afterNavigate } from '$app/navigation';
  import SendToSabDialog from '#lib/SendToSabDialog.svelte';
  import { getSpots } from '#lib/api.js';
  import { loadConfig } from '#lib/config.js';
  import { formatSize, formatDate, spotCat, getFormatEmoji, FILTERS, subCatItems, badgeHref } from '#lib/format.js';

  // ── State ──────────────────────────────────────────────────────────────────
  let spots = $state([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state('');

  let query = $state('');
  let selectedFilter = $state('all'); // a FILTERS key
  const activeFilter = $derived(FILTERS.find(f => f.key === selectedFilter) ?? FILTERS[0]);
  let page = $state(0);
  // Narrowing that comes from a link on a spot (poster, tag, a badge); each shows as a chip.
  let poster = $state('');
  let tag = $state('');
  let sub = $state(''); // one subcategory code, e.g. "a5"; its meaning depends on the filter
  const PAGE_SIZE = 50;

  let debounceTimer;
  let allowAdult = $state(false);
  let authenticated = $state(true); // default true; updated after config loads

  /** Sync current filter state into the URL without adding a history entry. */
  function syncURL() {
    const params = new URLSearchParams();
    if (query) params.set('q', query);
    if (selectedFilter !== 'all') params.set('f', selectedFilter);
    if (poster) params.set('poster', poster);
    if (tag) params.set('tag', tag);
    if (sub) params.set('sub', sub);
    if (page > 0) params.set('p', String(page));
    const search = params.toString();
    // Preserve SvelteKit's own history.state — replacing with {} wipes its
    // internal navigation state and causes back-button to land on a blank page.
    history.replaceState(history.state, '', search ? '/?' + search : '/');
  }

  // loadConfig, not getConfig: this runs before the layout's onMount has fetched the config, and
  // getConfig's defaults (allow_adult false) hid the 18+ tab.
  onMount(async () => {
    const cfg = await loadConfig();
    allowAdult = cfg.allow_adult;
    authenticated = cfg.authenticated ?? true;
  });

  // Read the filters from the URL on the first load AND after every navigation: a link from a
  // row on this page (a poster, a badge) lands on this same page, which is not mounted again.
  afterNavigate(async () => {
    const params = new URLSearchParams(window.location.search);
    query = params.get('q') ?? '';
    // ?f=comic; an older ?cat=1 link still selects Audio, Games or Apps.
    const legacyCat = params.get('cat');
    selectedFilter = params.get('f') ?? FILTERS.find(f => f.cat != null && String(f.cat) === legacyCat)?.key ?? 'all';
    page = parseInt(params.get('p') ?? '0');
    poster = params.get('poster') ?? '';
    tag = params.get('tag') ?? '';
    sub = params.get('sub') ?? '';
    await load();
    // On a phone the filter row scrolls sideways; a link can land on a tab that is off screen.
    await tick();
    document.querySelector('[data-active-filter]')?.scrollIntoView({ inline: 'center', block: 'nearest' });
  });

  // ── Loader ─────────────────────────────────────────────────────────────────
  async function load() {
    loading = true;
    error = '';
    try {
      const res = await getSpots({
        q: query || undefined,
        cat: activeFilter.cat,
        kind: activeFilter.kind,
        poster: poster || undefined,
        tag: tag || undefined,
        sub: sub || undefined,
        limit: PAGE_SIZE,
        offset: page * PAGE_SIZE,
      });
      spots = res.spots ?? [];
      total = res.total ?? 0;
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  function onQueryInput() {
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(() => { page = 0; syncURL(); load(); }, 350);
  }

  function selectFilter(key) {
    selectedFilter = key;
    sub = ''; // a subcategory code means something else in another category
    page = 0;
    syncURL();
    load();
  }

  function prevPage() { if (page > 0) { page--; syncURL(); load(); scrollToTop(); } }
  function nextPage() { if ((page + 1) * PAGE_SIZE < total) { page++; syncURL(); load(); scrollToTop(); } }
  function scrollToTop() { window.scrollTo({ top: 0, behavior: 'smooth' }); }

  let totalPages = $derived(Math.ceil(total / PAGE_SIZE));
  let currentPage = $derived(page + 1);

  // ── Helpers ────────────────────────────────────────────────────────────────
  function spotBadges(spot) {
    const badges = [];
    if (spot.Category === 0) {
      badges.push(...subCatItems(spot.SubCatA, 0, 'a'));
      badges.push(...subCatItems(spot.SubCatB, 0, 'b'));
      badges.push(...subCatItems(spot.SubCatC, 0, 'c').slice(0, 2));
    } else if (spot.Category === 1) {
      badges.push(...subCatItems(spot.SubCatA, 1, 'a'));
    } else if (spot.Category === 2 || spot.Category === 3) {
      badges.push(...subCatItems(spot.SubCatA, spot.Category, 'a'));
    }
    return badges.slice(0, 4);
  }

  // The label of the active badge filter, for its chip ("ePub" for a5 among books).
  const subLabel = $derived.by(() => {
    if (!sub) return '';
    const cat = activeFilter.cat ?? 0;
    return subCatItems(sub + '|', cat, sub[0])[0]?.label ?? sub;
  });

  function clearNarrowing(which) {
    if (which === 'poster') poster = '';
    if (which === 'tag') tag = '';
    if (which === 'sub') sub = '';
    page = 0;
    syncURL();
    load();
  }

  // SAB send: the dialog asks for category and password; the row shows the result briefly.
  let sending = $state({});
  let dialogSpot = $state(null);
  function sendToSAB(spot) {
    dialogSpot = spot;
  }
  function sent(spot) {
    sending[spot.ID] = 'ok';
    setTimeout(() => { delete sending[spot.ID]; sending = { ...sending }; }, 2000);
  }
</script>

<svelte:head>
  <title>Spottr</title>
</svelte:head>

<div class="max-w-7xl mx-auto px-4 py-4">

  <!-- Search + filters bar -->
  <div class="flex flex-col sm:flex-row gap-3 mb-4">
    <div class="relative flex-1">
      <svg class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-500 pointer-events-none"
           fill="none" stroke="currentColor" viewBox="0 0 24 24">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
          d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0"/>
      </svg>
      <input
        type="search"
        bind:value={query}
        oninput={onQueryInput}
        placeholder="Search titles, posters, tags…"
        class="input pl-9"
      />
    </div>

  </div>

  <!-- Category tabs -->
  <div class="flex gap-1 mb-5 overflow-x-auto pb-1 scrollbar-hide">
    {#each FILTERS.filter(f => !f.adult || allowAdult) as cat}
      {@const active = selectedFilter === cat.key}
      <button
        data-active-filter={active ? '' : undefined}
        onclick={() => selectFilter(cat.key)}
        class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium whitespace-nowrap transition-colors
               {active ? 'bg-orange-500 text-white' : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800'}"
      >
        <span>{cat.emoji}</span>
        <span>{cat.label}</span>
      </button>
    {/each}
  </div>

  {#if poster || tag || sub}
    <div class="flex flex-wrap gap-2 -mt-3 mb-4">
      {#each [['poster', 'Posted by', poster], ['tag', 'Tag', tag], ['sub', activeFilter.label, subLabel]] as [key, what, value]}
        {#if value}
          <button onclick={() => clearNarrowing(key)} title="Remove this filter"
                  class="inline-flex items-center gap-1.5 text-sm rounded-full pl-3 pr-2 py-1 bg-orange-500/15 text-orange-300 border border-orange-500/30 hover:bg-orange-500/25">
            <span class="text-orange-400/70">{what}:</span> {value}
            <span aria-hidden="true" class="text-orange-300/80">✕</span>
          </button>
        {/if}
      {/each}
    </div>
  {/if}

  <!-- Spot list -->
  {#if error}
    <div class="card p-6 text-center text-red-400">{error}</div>
  {:else if loading}
    <div class="space-y-2">
      {#each Array(8) as _}
        <div class="card p-4 animate-pulse flex gap-3">
          <div class="w-8 h-8 rounded-sm bg-slate-700/60 shrink-0"></div>
          <div class="flex-1 space-y-2">
            <div class="h-4 bg-slate-700/60 rounded-sm w-3/4"></div>
            <div class="h-3 bg-slate-700/40 rounded-sm w-1/3"></div>
          </div>
          <div class="w-16 h-4 bg-slate-700/40 rounded-sm self-start"></div>
        </div>
      {/each}
    </div>
  {:else if spots.length === 0}
    <div class="card p-12 text-center text-slate-500">
      <p class="text-lg mb-1">No spots found</p>
      <p class="text-sm">Try a different search or category</p>
    </div>
  {:else}
    <div class="space-y-px rounded-xl overflow-hidden border border-[#2a2d3a]">
      {#each spots as spot (spot.ID)}
        {@const cat = spotCat(spot)}
        {@const badges = spotBadges(spot)}
        <div class="bg-[#1a1d27] hover:bg-[#1f2335] transition-colors group flex items-start gap-3 px-4 py-3">
          <!-- Category icon -->
          <div class="shrink-0 w-8 h-8 rounded-md flex items-center justify-center text-lg {cat.bg} mt-0.5">
            {getFormatEmoji(spot)}
          </div>

          <!-- Main content -->
          <div class="flex-1 min-w-0">
            <a href="/spots/{spot.ID}"
               class="font-medium text-slate-100 hover:text-orange-400 transition-colors line-clamp-2 leading-snug wrap-anywhere">
              {spot.Title || '(no title)'}
            </a>
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1 mt-1">
              {#if spot.Poster}
                <a href="/?poster={encodeURIComponent(spot.Poster)}" class="text-xs text-slate-500 hover:text-slate-300 hover:underline">{spot.Poster}</a>
              {/if}
              {#if spot.Tag}
                <span class="text-xs text-slate-600">·</span>
                <a href="/?tag={encodeURIComponent(spot.Tag)}" class="text-xs text-slate-500 italic hover:text-slate-300 hover:underline">{spot.Tag}</a>
              {/if}
              {#each badges as b}
                {@const href = badgeHref(spot, b.code)}
                {#if href}
                  <a {href} class="badge bg-slate-700/60 text-slate-400 hover:bg-slate-600 hover:text-white transition-colors">{b.label}</a>
                {:else}
                  <span class="badge bg-slate-700/60 text-slate-400">{b.label}</span>
                {/if}
              {/each}
            </div>
          </div>

          <!-- Right: size + date + SAB button -->
          <div class="shrink-0 flex flex-col items-end gap-1 ml-2">
            <div class="flex items-center gap-2">
              <span class="text-xs text-slate-500 tabular-nums">{formatSize(spot.Size)}</span>
              <span class="text-xs text-slate-600">{formatDate(spot.PostedAt)}</span>
            </div>
            {#if authenticated}
            <button
              onclick={(e) => { e.stopPropagation(); sendToSAB(spot); }}
              disabled={!!sending[spot.ID]}
              title="Send to SABnzbd"
              class="pointer-fine:opacity-0 pointer-fine:group-hover:opacity-100 focus-visible:opacity-100 transition-opacity btn-sm px-2! py-1!
                     {sending[spot.ID] === 'ok' ? 'bg-green-700!' : sending[spot.ID] === 'err' ? 'bg-red-700!' : ''}"
            >
              {#if sending[spot.ID] === 'ok'}✓
              {:else if sending[spot.ID] === 'err'}✗
              {:else if sending[spot.ID]}…
              {:else}
                <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                    d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"/>
                </svg>
              {/if}
            </button>
            {/if}
          </div>
        </div>
      {/each}
    </div>

    <!-- Pagination -->
    {#if totalPages > 1}
      <div class="flex items-center justify-between mt-4 text-sm text-slate-500">
        <span>{total.toLocaleString()} spots · page {currentPage} of {totalPages}</span>
        <div class="flex gap-2">
          <button onclick={prevPage} disabled={page === 0} class="btn-sm">← Prev</button>
          <button onclick={nextPage} disabled={currentPage >= totalPages} class="btn-sm">Next →</button>
        </div>
      </div>
    {:else}
      <p class="mt-3 text-sm text-slate-600">{total.toLocaleString()} spots</p>
    {/if}
  {/if}
</div>

{#if dialogSpot}
  {@const spot = dialogSpot}
  <SendToSabDialog {spot} onclose={() => (dialogSpot = null)} onsent={() => sent(spot)} />
{/if}
