<!--
  Send a spot to SABnzbd with a name, a category and an archive password. Shaped like SecretHive's
  dialog: Escape and the cross close it, a click beside it does not (a stray click would throw away
  what was typed), and focus moves into it when it opens.

  The password is prefilled only from what the poster wrote in the description (PasswordHint from
  the API), and says so; it is a suggestion, always visible before sending.
-->
<script>
  import { onMount } from 'svelte';
  import { getSpot, getSabCategories, sendToSAB } from '#lib/api.js';

  /** @type {{ spot: { ID: number, Title: string, PasswordHint?: string }, onclose: () => void, onsent?: () => void }} */
  let { spot, onclose, onsent } = $props();

  const LAST_CATEGORY = 'spottr.sab.category';

  let name = $state(spot.Title);
  let category = $state('*');
  let categories = $state([]);
  let password = $state(spot.PasswordHint ?? '');
  let fromDescription = $state(!!spot.PasswordHint);
  let checkingDescription = $state(spot.PasswordHint === undefined);
  let showPassword = $state(true);
  let sending = $state(false);
  let error = $state('');

  onMount(async () => {
    try {
      const saved = localStorage.getItem(LAST_CATEGORY);
      if (saved) category = saved;
    } catch {}

    try {
      categories = await getSabCategories();
      if (!categories.includes(category)) category = categories[0] ?? '*';
    } catch (e) {
      error = `Could not load SABnzbd's categories: ${e.message}`;
    }

    // Opened from the list: the description is not loaded yet. Opening the spot reads it.
    if (spot.PasswordHint === undefined) {
      try {
        const full = await getSpot(spot.ID);
        if (full.PasswordHint && !password) {
          password = full.PasswordHint;
          fromDescription = true;
        }
      } catch {}
      checkingDescription = false;
    }
  });

  async function submit(e) {
    e.preventDefault();
    sending = true;
    error = '';
    try {
      await sendToSAB(spot.ID, { name: name.trim(), category: category === '*' ? '' : category, password: password.trim() });
      try { localStorage.setItem(LAST_CATEGORY, category); } catch {}
      onsent?.();
      onclose();
    } catch (err) {
      error = err.message;
      sending = false;
    }
  }

  function onkeydown(e) {
    if (e.key === 'Escape') onclose();
  }
</script>

<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-xs p-4">
  <div class="card w-full max-w-md shadow-2xl outline-hidden" role="dialog" aria-modal="true" aria-label="Send to SABnzbd"
       tabindex="-1" {onkeydown} {@attach (node) => node.focus()}>
    <div class="flex items-center justify-between gap-3 px-5 py-4 border-b border-[#2a2d3a]">
      <h2 class="text-base font-semibold text-slate-100">Send to SABnzbd</h2>
      <button type="button" class="btn-ghost px-2! py-1!" title="Close" onclick={onclose}>
        <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24" aria-hidden="true">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/>
        </svg>
        <span class="sr-only">Close</span>
      </button>
    </div>

    <form class="px-5 py-4 space-y-4" onsubmit={submit}>
      <label class="block">
        <span class="block text-xs font-medium uppercase tracking-wide text-slate-500 mb-1.5">Name</span>
        <input class="input" bind:value={name} required />
      </label>

      <label class="block">
        <span class="block text-xs font-medium uppercase tracking-wide text-slate-500 mb-1.5">Category</span>
        <select class="input" bind:value={category} disabled={!categories.length}>
          {#each categories as c}
            <option value={c}>{c === '*' ? 'Default (*)' : c}</option>
          {/each}
        </select>
      </label>

      <div>
        <label class="block" for="sab-password">
          <span class="block text-xs font-medium uppercase tracking-wide text-slate-500 mb-1.5">Archive password</span>
        </label>
        <div class="flex gap-2">
          <input id="sab-password" class="input font-mono" type={showPassword ? 'text' : 'password'} autocomplete="off"
                 spellcheck="false" bind:value={password} oninput={() => (fromDescription = false)}
                 placeholder={checkingDescription ? 'Reading the description…' : 'None'} />
          <button type="button" class="btn-sm shrink-0" onclick={() => (showPassword = !showPassword)}
                  title={showPassword ? 'Hide' : 'Show'}>{showPassword ? 'Hide' : 'Show'}</button>
        </div>
        {#if fromDescription && password}
          <p class="text-xs text-orange-400/90 mt-1.5">Found in the spot's description. Check it before sending.</p>
        {:else if !checkingDescription && !password}
          <p class="text-xs text-slate-500 mt-1.5">The description gives none. Leave empty unless you know one.</p>
        {/if}
      </div>

      {#if error}
        <p class="text-sm text-red-400">{error}</p>
      {/if}

      <div class="flex justify-end gap-2 pt-1">
        <button type="button" class="btn-ghost" onclick={onclose}>Cancel</button>
        <button type="submit" class="btn-primary" disabled={sending || !name.trim()}>
          {sending ? 'Sending…' : 'Send'}
        </button>
      </div>
    </form>
  </div>
</div>
