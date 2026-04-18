<script>
  import { onMount, tick } from 'svelte';
  import { getSpots } from '$lib/api.js';
  import { getConfig } from '$lib/config.js';
  import { formatSize, formatDate, getCat, getFormatEmoji, CATEGORIES, parseSubCat } from '$lib/format.js';

  // ── State ──────────────────────────────────────────────────────────────────
  let spots = [];
  let total = 0;
  let loading = true;
  let error = '';

  let query = '';
  let selectedCat = null;   // null = all
  let page = 0;
  let adult = false;
  const PAGE_SIZE = 50;

  let debounceTimer;
  let allowAdult = false;
  let authenticated = true; // default true; updated after config loads

  /** Sync current filter state into the URL without adding a history entry. */
  function syncURL() {
    const params = new URLSearchParams();
    if (query) params.set('q', query);
    if (selectedCat != null) params.set('cat', String(selectedCat));
    if (page > 0) params.set('p', String(page));
    if (adult) params.set('adult', '1');
    const search = params.toString();
    // Preserve SvelteKit's own history.state — replacing with {} wipes its
    // internal navigation state and causes back-button to land on a blank page.
    history.replaceState(history.state, '', search ? '/?' + search : '/');
  }

  onMount(async () => {
    // Restore filter state from URL (back-button support)
    const params = new URLSearchParams(window.location.search);
    query = params.get('q') ?? '';
    const catParam = params.get('cat');
    selectedCat = catParam !== null ? parseInt(catParam) : null;
    page = parseInt(params.get('p') ?? '0');
    adult = params.get('adult') === '1';

    allowAdult = getConfig().allow_adult;
    authenticated = getConfig().authenticated ?? true;
    await load();
  });

  // ── Loader ─────────────────────────────────────────────────────────────────
  async function load() {
    loading = true;
    error = '';
    try {
      const res = await getSpots({
        q: query || undefined,
        cat: selectedCat,
        limit: PAGE_SIZE,
        offset: page * PAGE_SIZE,
        adult: adult || undefined,
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

  function selectCat(id) {
    selectedCat = id;
    page = 0;
    syncURL();
    load();
  }

  function prevPage() { if (page > 0) { page--; syncURL(); load(); scrollToTop(); } }
  function nextPage() { if ((page + 1) * PAGE_SIZE < total) { page++; syncURL(); load(); scrollToTop(); } }
  function scrollToTop() { window.scrollTo({ top: 0, behavior: 'smooth' }); }

  $: totalPages = Math.ceil(total / PAGE_SIZE);
  $: currentPage = page + 1;

  // ── Helpers ────────────────────────────────────────────────────────────────
  function spotBadges(spot) {
    const badges = [];
    if (spot.Category === 0) {
      badges.push(...parseSubCat(spot.SubCatA, 0, 'a'));
      badges.push(...parseSubCat(spot.SubCatB, 0, 'b'));
      badges.push(...parseSubCat(spot.SubCatC, 0, 'c').slice(0, 2));
    } else if (spot.Category === 1) {
      badges.push(...parseSubCat(spot.SubCatA, 1, 'a'));
    } else if (spot.Category === 2 || spot.Category === 3) {
      badges.push(...parseSubCat(spot.SubCatA, spot.Category, 'a'));
    }
    return badges.slice(0, 4);
  }

  function nzbUrl(spot) {
    return `/v1/spots/${spot.ID}/nzb`;
  }

  // SAB send
  let sending = {};
  async function sendToSAB(spot) {
    const { sendToSAB: api } = await import('$lib/api.js');
    sending[spot.ID] = true;
    try {
      await api(spot.ID, {
        nzb_url: window.location.origin + nzbUrl(spot),
        name: spot.Title,
        category: ''
      });
      // brief visual feedback
      sending[spot.ID] = 'ok';
      setTimeout(() => { delete sending[spot.ID]; sending = { ...sending }; }, 2000);
    } catch (e) {
      sending[spot.ID] = 'err';
      setTimeout(() => { delete sending[spot.ID]; sending = { ...sending }; }, 3000);
    }
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
        on:input={onQueryInput}
        placeholder="Search titles, posters, tags…"
        class="input pl-9"
      />
    </div>

    {#if allowAdult}
      <label class="flex items-center gap-2 text-sm text-slate-400 cursor-pointer self-center">
        <input type="checkbox" bind:checked={adult}
               on:change={() => { page = 0; syncURL(); load(); }}
               class="w-4 h-4 accent-orange-500 rounded" />
        18+
      </label>
    {/if}
  </div>

  <!-- Category tabs -->
  <div class="flex gap-1 mb-5 overflow-x-auto pb-1 scrollbar-hide">
    {#each CATEGORIES as cat}
      {@const active = selectedCat === cat.id}
      <button
        on:click={() => selectCat(cat.id)}
        class="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-sm font-medium whitespace-nowrap transition-colors
               {active ? 'bg-orange-500 text-white' : 'text-slate-400 hover:text-slate-200 hover:bg-slate-800'}"
      >
        <span>{cat.emoji}</span>
        <span>{cat.label}</span>
      </button>
    {/each}
  </div>

  <!-- Spot list -->
  {#if error}
    <div class="card p-6 text-center text-red-400">{error}</div>
  {:else if loading}
    <div class="space-y-2">
      {#each Array(8) as _}
        <div class="card p-4 animate-pulse flex gap-3">
          <div class="w-8 h-8 rounded bg-slate-700/60 shrink-0"></div>
          <div class="flex-1 space-y-2">
            <div class="h-4 bg-slate-700/60 rounded w-3/4"></div>
            <div class="h-3 bg-slate-700/40 rounded w-1/3"></div>
          </div>
          <div class="w-16 h-4 bg-slate-700/40 rounded self-start"></div>
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
        {@const cat = getCat(spot.Category)}
        {@const badges = spotBadges(spot)}
        <div class="bg-[#1a1d27] hover:bg-[#1f2335] transition-colors group flex items-start gap-3 px-4 py-3">
          <!-- Category icon -->
          <div class="shrink-0 w-8 h-8 rounded-md flex items-center justify-center text-lg {cat.bg} mt-0.5">
            {getFormatEmoji(spot)}
          </div>

          <!-- Main content -->
          <div class="flex-1 min-w-0">
            <a href="/spots/{spot.ID}"
               class="font-medium text-slate-100 hover:text-orange-400 transition-colors line-clamp-2 leading-snug">
              {spot.Title || '(no title)'}
            </a>
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1 mt-1">
              {#if spot.Poster}
                <span class="text-xs text-slate-500">{spot.Poster}</span>
              {/if}
              {#if spot.Tag}
                <span class="text-xs text-slate-600">·</span>
                <span class="text-xs text-slate-500 italic">{spot.Tag}</span>
              {/if}
              {#each badges as b}
                <span class="badge bg-slate-700/60 text-slate-400">{b}</span>
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
              on:click|stopPropagation={() => sendToSAB(spot)}
              disabled={!!sending[spot.ID]}
              title="Send to SABnzbd"
              class="opacity-0 group-hover:opacity-100 transition-opacity btn-sm !px-2 !py-1
                     {sending[spot.ID] === 'ok' ? '!bg-green-700' : sending[spot.ID] === 'err' ? '!bg-red-700' : ''}"
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
          <button on:click={prevPage} disabled={page === 0} class="btn-sm">← Prev</button>
          <button on:click={nextPage} disabled={currentPage >= totalPages} class="btn-sm">Next →</button>
        </div>
      </div>
    {:else}
      <p class="mt-3 text-sm text-slate-600">{total.toLocaleString()} spots</p>
    {/if}
  {/if}
</div>
