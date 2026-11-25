<script lang="ts">
	import { api, type Card, type Me, type SourceType } from '#lib/api.ts';
	import CardTile from '#lib/components/CardTile.svelte';
	import SourcePicker from '#lib/components/SourcePicker.svelte';
	import Connections from '#lib/components/Connections.svelte';

	let me = $state<Me | null>(null);
	let cards = $state<Card[] | null>(null);
	let loadError = $state('');
	let picking = $state(false);
	let justCreated = $state<Card | null>(null);
	let menu = $state<HTMLDetailsElement>();

	$effect(() => {
		Promise.all([api.me(), api.cards()]).then(
			([m, c]) => {
				me = m;
				cards = c;
			},
			(e) => (loadError = e.message)
		);
	});

	async function create(sourceType: SourceType, sourceId: string, title: string) {
		const card = await api.createCard(sourceType, sourceId, title);
		cards = [...(cards ?? []), card].sort((a, b) => a.title.localeCompare(b.title));
		justCreated = card.yotoCardId ? card : null;
	}

	function replace(card: Card) {
		cards = (cards ?? []).map((c) => (c.id === card.id ? card : c));
	}

	function removed(id: number) {
		cards = (cards ?? []).filter((c) => c.id !== id);
	}

	// While audiobook chapters are being prepared, refresh card progress.
	const preparing = $derived((cards ?? []).some((c) => c.cache?.running));
	$effect(() => {
		if (!preparing) return;
		const t = setInterval(async () => {
			try {
				cards = await api.cards();
			} catch {
				/* keep the last list */
			}
		}, 3000);
		return () => clearInterval(t);
	});

	async function reload() {
		[me, cards] = await Promise.all([api.me(), api.cards()]);
	}

	const initial = $derived((me?.name || me?.user || '?').trim().charAt(0).toUpperCase());

	// Close the account menu on outside taps.
	function onWindowClick(e: MouseEvent) {
		if (menu?.open && !menu.contains(e.target as Node)) menu.open = false;
	}
</script>

<svelte:window onclick={onWindowClick} />

<div class="page">
	<header class="top">
		<div class="brand">
			<picture>
				<source srcset="/logo-dark.svg" media="(prefers-color-scheme: dark)" />
				<img class="logo" src="/logo.svg" alt="" width="36" height="36" />
			</picture>
			<h1>Yoto Proxy</h1>
		</div>
		{#if me}
			<details class="account" bind:this={menu}>
				<summary aria-label="Account">
					<span class="name">{me.name}</span>
					<span class="avatar" aria-hidden="true">{initial}</span>
				</summary>
				<div class="menu">
					<p class="menu-name">{me.name}</p>
					{#if me.user && me.user !== me.name}<p class="muted menu-user">{me.user}</p>{/if}
					{#if !me.authDisabled}
						<form method="POST" action="/auth/logout"><button class="btn small">Log out</button></form>
					{/if}
				</div>
			</details>
		{/if}
	</header>

	{#if loadError}
		<p class="banner bad">{loadError}</p>
	{:else if !me}
		<div class="skeleton conn-skel"></div>
		<div class="grid">
			<div class="skeleton tile-skel"></div>
			<div class="skeleton tile-skel"></div>
		</div>
	{:else}
		{#if me.yoto.connected}
			<Connections {me} onchange={reload} />
		{:else}
			<section class="banner connect">
				<div>
					<h2>{me.yoto.label ? `Reconnect “${me.yoto.label}”` : 'Connect your Yoto account'}</h2>
					<p class="muted">
						Cards are created as playlists in your Yoto library, so this app needs permission to
						manage your Make Your Own content. Sharing someone else’s Yoto? Ask them for an invite
						link instead.
					</p>
				</div>
				<a class="btn primary" href="/auth/yoto/login">Connect Yoto</a>
			</section>
			{#if !me.navidromeOk}
				<p class="banner bad">Navidrome error: {me.navidromeError}</p>
			{/if}
		{/if}

		{#if justCreated}
			<section class="banner tip">
				<div>
					<h2>“{justCreated.title}” is in your Yoto library</h2>
					<p>
						To put it on a physical card: in the Yoto app, open <strong>Library</strong>, choose the
						playlist, tap <strong>Link to card</strong>, then insert a blank Make Your Own card into the
						player. After that, <strong>Sync now</strong> updates the card in place.
					</p>
				</div>
				<button class="btn small" onclick={() => (justCreated = null)}>Got it</button>
			</section>
		{/if}

		<section class="cards-head">
			<h2>Cards{#if cards?.length}<span class="count">{cards.length}</span>{/if}</h2>
			<button class="btn primary" onclick={() => (picking = true)} disabled={!me.yoto.connected}>
				<span aria-hidden="true" class="plus">+</span> New card
			</button>
		</section>

		{#if cards && cards.length === 0}
			<div class="empty">
				<p><strong>No cards yet.</strong></p>
				<p class="muted">
					Pick a Navidrome playlist or album{me.audiobookshelf.connected ? ', or an audiobook,' : ''} and it
					becomes a Yoto playlist you can link to a card.
				</p>
				{#if me.audiobookshelf.configured && !me.audiobookshelf.connected && me.yoto.connected}
					<p class="muted">
						Want audiobooks too? <a href="/auth/abs/login">Connect Audiobookshelf</a>.
					</p>
				{/if}
			</div>
		{:else if cards}
			<div class="grid">
				{#each cards as card (card.id)}
					<CardTile {card} onchange={replace} ondelete={removed} />
				{/each}
			</div>
		{/if}
	{/if}
</div>

{#if picking}
	<SourcePicker
		absConnected={!!me?.audiobookshelf.connected}
		onclose={() => (picking = false)}
		oncreate={create}
	/>
{/if}

<style>
	.page {
		max-width: 1080px;
		margin: 0 auto;
		padding: calc(1rem + env(safe-area-inset-top)) calc(16px + env(safe-area-inset-right)) calc(3rem + env(safe-area-inset-bottom))
			calc(16px + env(safe-area-inset-left));
	}
	.top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		margin-bottom: 1rem;
	}
	.brand {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		min-width: 0;
	}
	.brand h1 {
		font-size: 1.35rem;
		letter-spacing: -0.01em;
		white-space: nowrap;
	}
	picture {
		display: contents;
	}
	.logo {
		width: 36px;
		height: 36px;
		flex-shrink: 0;
	}
	.account {
		position: relative;
	}
	.account summary {
		list-style: none;
		display: flex;
		align-items: center;
		gap: 0.5rem;
		cursor: pointer;
		padding: 2px;
		border-radius: 999px;
	}
	.account summary::-webkit-details-marker {
		display: none;
	}
	.account summary:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
	}
	.name {
		font-size: 0.9rem;
		color: var(--muted);
	}
	.avatar {
		display: grid;
		place-items: center;
		width: 36px;
		height: 36px;
		border-radius: 50%;
		background: var(--accent-soft);
		color: var(--accent);
		font-weight: 700;
	}
	.menu {
		position: absolute;
		right: 0;
		top: calc(100% + 6px);
		min-width: 200px;
		padding: 0.8rem;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 12px;
		box-shadow: var(--shadow);
		z-index: 5;
	}
	.menu p {
		margin: 0;
	}
	.menu-name {
		font-weight: 700;
	}
	.menu-user {
		font-size: 0.85rem;
	}
	.menu form {
		margin: 0.6rem 0 0;
	}
	.menu .btn {
		width: 100%;
	}
	.banner {
		display: flex;
		gap: 1rem;
		align-items: center;
		justify-content: space-between;
		padding: 1rem 1.1rem;
		border-radius: var(--radius);
		margin: 0 0 1rem;
		border: 1px solid var(--border);
		background: var(--surface);
	}
	.banner h2 {
		font-size: 1.05rem;
		margin-bottom: 0.25rem;
	}
	.banner p {
		margin: 0;
	}
	.banner.bad {
		background: var(--danger-soft);
		color: var(--danger);
		border-color: transparent;
	}
	.banner.connect,
	.banner.tip {
		background: var(--accent-soft);
		border-color: transparent;
	}
	.cards-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin: 1.25rem 0 0.9rem;
	}
	.cards-head h2 {
		font-size: 1.25rem;
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}
	.count {
		font-size: 0.8rem;
		font-weight: 700;
		padding: 0.15em 0.6em;
		border-radius: 999px;
		background: var(--surface-2);
		color: var(--muted);
	}
	.plus {
		font-size: 1.2em;
		line-height: 1;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(190px, 1fr));
		gap: 1rem;
	}
	.empty {
		text-align: center;
		padding: 2.5rem 1rem;
		border: 2px dashed var(--border);
		border-radius: var(--radius);
	}
	.empty p {
		margin: 0.25rem 0;
	}
	.skeleton {
		border-radius: var(--radius);
		background: var(--surface-2);
		animation: pulse 1.2s ease-in-out infinite alternate;
	}
	.conn-skel {
		height: 62px;
		margin-bottom: 2.25rem;
	}
	.tile-skel {
		aspect-ratio: 5 / 9;
	}
	@keyframes pulse {
		to {
			opacity: 0.5;
		}
	}
	@media (max-width: 560px) {
		.name {
			display: none;
		}
		.banner {
			flex-direction: column;
			align-items: stretch;
		}
		.grid {
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: 0.65rem;
		}
	}
</style>
