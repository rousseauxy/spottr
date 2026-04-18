<script>
  import '../app.css';
  import { onMount } from 'svelte';
  import { writable } from 'svelte/store';
  import { loadConfig, getConfig, login, logout } from '$lib/config.js';
  import { getQueue } from '$lib/api.js';

  // Exported so child pages can read authenticated state reactively
  export const authStore = writable({ auth_required: false, authenticated: true });

  let queueCount = 0;
  let cfg = { allow_adult: false, auth_required: false, authenticated: true };

  // Login modal state
  let showLogin = false;
  let loginPassword = '';
  let loginError = '';
  let loginLoading = false;

  onMount(async () => {
    cfg = await loadConfig();
    authStore.set({ auth_required: cfg.auth_required, authenticated: cfg.authenticated });
    if (cfg.auth_required && cfg.authenticated) {
      refreshQueue();
      const t = setInterval(refreshQueue, 30_000);
      return () => clearInterval(t);
    }
  });

  async function refreshQueue() {
    try {
      const q = await getQueue();
      queueCount = q?.slots?.length ?? 0;
    } catch { queueCount = 0; }
  }

  async function doLogin() {
    loginLoading = true;
    loginError = '';
    try {
      await login(loginPassword);
      cfg = getConfig();
      authStore.set({ auth_required: cfg.auth_required, authenticated: cfg.authenticated });
      showLogin = false;
      loginPassword = '';
      refreshQueue();
      const t = setInterval(refreshQueue, 30_000);
      return () => clearInterval(t);
    } catch (e) {
      loginError = e.message;
    } finally {
      loginLoading = false;
    }
  }

  async function doLogout() {
    await logout();
    cfg = getConfig();
    authStore.set({ auth_required: cfg.auth_required, authenticated: cfg.authenticated });
    queueCount = 0;
  }

  function onLoginKeydown(e) {
    if (e.key === 'Enter') doLogin();
    if (e.key === 'Escape') { showLogin = false; loginPassword = ''; loginError = ''; }
  }
</script>

<div class="min-h-screen flex flex-col">
  <!-- Header -->
  <header class="sticky top-0 z-40 border-b border-[#2a2d3a] bg-[#0f1117]/95 backdrop-blur supports-[backdrop-filter]:bg-[#0f1117]/80">
    <div class="max-w-7xl mx-auto px-4 h-14 flex items-center gap-4">
      <a href="/" class="flex items-center gap-2 shrink-0">
        <span class="text-orange-500 text-xl font-bold tracking-tight">Spottr</span>
      </a>

      <div class="flex-1"></div>

      <!-- SAB queue indicator (authenticated only) -->
      {#if cfg.authenticated && queueCount > 0}
        <a href="/queue" class="flex items-center gap-1.5 text-sm text-slate-400 hover:text-slate-200 transition-colors">
          <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
              d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4"/>
          </svg>
          <span class="bg-orange-500 text-white text-xs font-bold px-1.5 py-0.5 rounded-full">{queueCount}</span>
        </a>
      {/if}

      <!-- Auth button -->
      {#if cfg.auth_required}
        {#if cfg.authenticated}
          <button on:click={doLogout}
            class="text-sm text-slate-500 hover:text-slate-300 transition-colors px-2 py-1 rounded">
            Sign out
          </button>
        {:else}
          <button on:click={() => showLogin = true}
            class="text-sm font-medium bg-orange-500 hover:bg-orange-400 text-white px-3 py-1.5 rounded-lg transition-colors">
            Sign in
          </button>
        {/if}
      {/if}
    </div>
  </header>

  <!-- Page content -->
  <main class="flex-1">
    <slot />
  </main>
</div>

<!-- Login modal -->
{#if showLogin}
  <!-- svelte-ignore a11y-click-events-have-key-events a11y-no-static-element-interactions -->
  <div class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
       on:click|self={() => { showLogin = false; loginPassword = ''; loginError = ''; }}>
    <div class="bg-[#1a1d27] border border-[#2a2d3a] rounded-2xl p-8 w-full max-w-sm shadow-2xl">
      <h2 class="text-lg font-semibold text-slate-100 mb-1">Sign in</h2>
      <p class="text-sm text-slate-500 mb-5">Enter your password to continue</p>

      <input
        type="password"
        bind:value={loginPassword}
        on:keydown={onLoginKeydown}
        placeholder="Password"
        autofocus
        class="input w-full mb-3"
      />

      {#if loginError}
        <p class="text-sm text-red-400 mb-3">{loginError}</p>
      {/if}

      <button
        on:click={doLogin}
        disabled={loginLoading || !loginPassword}
        class="w-full py-2.5 rounded-lg font-medium text-sm transition-colors
               {loginLoading || !loginPassword
                 ? 'bg-slate-700 text-slate-500 cursor-not-allowed'
                 : 'bg-orange-500 hover:bg-orange-400 text-white'}">
        {loginLoading ? 'Signing in…' : 'Sign in'}
      </button>
    </div>
  </div>
{/if}

