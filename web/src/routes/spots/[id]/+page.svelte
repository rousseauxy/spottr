<script>
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { getSpot, sendToSAB } from '$lib/api.js';
  import { getConfig } from '$lib/config.js';
  import { formatSize, formatDate, getCat, getFormatEmoji, parseSubCatSections, renderBBCode } from '$lib/format.js';

  let spot = null;
  let loading = true;
  let error = '';
  let sabState = '';  // '' | 'sending' | 'ok' | 'err'
  let sabError = '';
  let authenticated = true;

  onMount(async () => {
    authenticated = getConfig().authenticated ?? true;
    try {
      spot = await getSpot(Number($page.params.id));
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  });

  async function handleSendToSAB() {
    sabState = 'sending';
    sabError = '';
    try {
      await sendToSAB(spot.ID, {
        nzb_url: window.location.origin + `/v1/spots/${spot.ID}/nzb`,
        name: spot.Title,
        category: ''
      });
      sabState = 'ok';
    } catch (e) {
      sabState = 'err';
      sabError = e.message;
    }
  }
</script>

<svelte:head>
  <title>{spot?.Title ?? 'Spot'} · Spottr</title>
</svelte:head>

<div class="max-w-3xl mx-auto px-4 py-6">
  <!-- Back -->
  <a href="/" class="inline-flex items-center gap-1.5 text-sm text-slate-500 hover:text-slate-300 transition-colors mb-5">
    <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
      <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7"/>
    </svg>
    Back
  </a>

  {#if loading}
    <div class="card p-8 animate-pulse space-y-4">
      <div class="h-6 bg-slate-700/60 rounded w-3/4"></div>
      <div class="h-4 bg-slate-700/40 rounded w-1/2"></div>
      <div class="h-32 bg-slate-700/30 rounded"></div>
    </div>

  {:else if error}
    <div class="card p-8 text-center text-red-400">{error}</div>

  {:else if spot}
    {@const cat = getCat(spot.Category)}
    {@const sections = parseSubCatSections(spot)}

    <div class="card overflow-hidden">
      <!-- Header band -->
      <div class="px-6 py-4 border-b border-[#2a2d3a] flex items-start gap-4">
        <div class="shrink-0 w-12 h-12 rounded-xl flex items-center justify-center text-2xl {cat.bg}">
          {getFormatEmoji(spot)}
        </div>
        <div class="flex-1 min-w-0">
          <h1 class="text-lg font-semibold text-slate-100 leading-snug">{spot.Title}</h1>
          <div class="flex flex-wrap items-center gap-x-3 mt-1 text-sm text-slate-500">
            <span class="{cat.color} font-medium">{cat.label}</span>
            {#if spot.Poster}<span>by {spot.Poster}</span>{/if}
            {#if spot.Tag}<span class="italic">{spot.Tag}</span>{/if}
          </div>
        </div>
      </div>

      <!-- Image -->
      {#if spot.ImageURL}
        <div class="px-6 pt-4">
          <img src="/v1/spots/{spot.ID}/image" alt={spot.Title}
               class="rounded-lg max-h-80 object-contain w-full bg-slate-900" loading="lazy" />
        </div>
      {/if}

      <!-- Meta grid -->
      <div class="grid grid-cols-2 sm:grid-cols-3 gap-4 px-6 py-4 border-b border-[#2a2d3a]">
        <div>
          <p class="text-xs text-slate-600 uppercase tracking-wider mb-0.5">Size</p>
          <p class="text-sm font-medium text-slate-200">{formatSize(spot.Size)}</p>
        </div>
        <div>
          <p class="text-xs text-slate-600 uppercase tracking-wider mb-0.5">Posted</p>
          <p class="text-sm font-medium text-slate-200">{new Date(spot.PostedAt).toLocaleDateString('en-GB', { day:'2-digit', month:'short', year:'numeric', hour:'2-digit', minute:'2-digit' })}</p>
        </div>
        {#if spot.NzbID}
          <div>
            <p class="text-xs text-slate-600 uppercase tracking-wider mb-0.5">NZB ID</p>
            <p class="text-sm font-medium text-slate-400 font-mono truncate" title={spot.NzbID}>{spot.NzbID.slice(0, 20)}…</p>
          </div>
        {/if}
      </div>

      <!-- SubCat sections -->
      {#if sections.length}
        <div class="px-6 py-3 border-b border-[#2a2d3a] space-y-2">
          {#each sections as sec}
            <div class="flex flex-wrap items-center gap-1.5">
              <span class="text-xs text-slate-600 uppercase tracking-wider w-20 shrink-0">{sec.label}</span>
              {#each sec.values as v}
                <span class="badge bg-slate-700/60 text-slate-300">{v}</span>
              {/each}
            </div>
          {/each}
        </div>
      {/if}

      <!-- Description -->
      {#if spot.Description}
        <div class="px-6 py-4 border-b border-[#2a2d3a]">
          <p class="text-xs text-slate-600 uppercase tracking-wider mb-2">Description</p>
          <div class="text-sm text-slate-300 leading-relaxed bbcode">{@html renderBBCode(spot.Description)}</div>
        </div>
      {/if}

      <!-- Actions -->
      <div class="px-6 py-4 flex flex-wrap gap-3 items-center">
        {#if authenticated}
        <button
          on:click={handleSendToSAB}
          disabled={sabState === 'sending' || sabState === 'ok'}
          class="btn-primary"
        >
          {#if sabState === 'sending'}
            <svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24">
              <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"/>
              <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v8z"/>
            </svg>
            Sending…
          {:else if sabState === 'ok'}
            ✓ Added to SABnzbd
          {:else}
            <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
                d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"/>
            </svg>
            Send to SABnzbd
          {/if}
        </button>
        {/if}

        <a href="/v1/spots/{spot.ID}/nzb"
           class="btn-sm"
           download="{spot.Title}.nzb">
          ⬇ Download NZB
        </a>

        {#if sabState === 'err'}
          <p class="text-sm text-red-400">{sabError}</p>
        {/if}
      </div>
    </div>

    <!-- Debug / raw message id -->
    <p class="mt-3 text-xs text-slate-700 font-mono truncate">{spot.MessageID}</p>
  {/if}
</div>
