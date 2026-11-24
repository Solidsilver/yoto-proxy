<script lang="ts">
	import { api, type Card, type Me, type SourceType } from '#lib/api.ts';
	import CardTile from '#lib/components/CardTile.svelte';
	import SourcePicker from '#lib/components/SourcePicker.svelte';
	import YotoAccount from '#lib/components/YotoAccount.svelte';

	let me = $state<Me | null>(null);
	let cards = $state<Card[] | null>(null);
	let loadError = $state('');
	let picking = $state(false);
	let justCreated = $state<Card | null>(null);

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

	async function disconnectAbs() {
		if (!confirm('Disconnect Audiobookshelf? Audiobook cards stop playing until you connect again.')) return;
		await api.disconnectAbs();
		me = await api.me();
	}

	async function reload() {
		[me, cards] = await Promise.all([api.me(), api.cards()]);
	}
</script>

<div class="page">
	<header class="top">
		<div class="brand">
			<picture>
				<source srcset="/logo-dark.svg" media="(prefers-color-scheme: dark)" />
				<img class="logo" src="/logo.svg" alt="" width="40" height="40" />
			</picture>
			<h1>Yoto Proxy</h1>
		</div>
		{#if me}
			<div class="who">
				<span class="muted">{me.name}</span>
				{#if !me.authDisabled}
					<form method="POST" action="/auth/logout"><button class="btn small">Log out</button></form>
				{/if}
			</div>
		{/if}
	</header>

	{#if loadError}
		<p class="banner bad">{loadError}</p>
	{:else if !me}
		<p class="muted">Loading…</p>
	{:else}
		<section class="status-row">
			<div class="pill" class:ok={me.navidromeOk} class:bad={!me.navidromeOk}>
				<span class="dot"></span> Navidrome {me.navidromeOk ? 'connected' : 'unreachable'}{me.navidromeUser
					? ` as ${me.navidromeUser}`
					: ''}
			</div>
			<div class="pill" class:ok={me.yoto.connected} class:warn={!me.yoto.connected}>
				<span class="dot"></span> Yoto {me.yoto.connected ? 'connected' : 'not connected'}
			</div>
			{#if me.audiobookshelf.configured}
				<div class="pill" class:ok={me.audiobookshelf.connected} class:warn={!me.audiobookshelf.connected}>
					<span class="dot"></span>
					Audiobookshelf {me.audiobookshelf.connected ? `as ${me.audiobookshelf.username}` : 'not connected'}
				</div>
			{/if}
			{#if me.audiobookshelf.connected}
				<button class="btn small" onclick={disconnectAbs}>Disconnect Audiobookshelf</button>
			{/if}
		</section>

		{#if !me.navidromeOk}
			<p class="banner bad">Navidrome error: {me.navidromeError}</p>
		{/if}

		{#if me.yoto.connected}
			<YotoAccount {me} onchange={reload} />
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
		{/if}

		{#if me.yoto.connected && me.audiobookshelf.configured && !me.audiobookshelf.connected}
			<section class="banner connect">
				<div>
					<h2>Add audiobooks</h2>
					<p class="muted">
						Connect your Audiobookshelf account (signs in with Pocket ID) to put books on cards.
					</p>
				</div>
				<a class="btn primary" href="/auth/abs/login">Connect Audiobookshelf</a>
			</section>
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
			<h2>Cards</h2>
			<button class="btn primary" onclick={() => (picking = true)} disabled={!me.yoto.connected}>
				+ New card
			</button>
		</section>

		{#if cards && cards.length === 0}
			<div class="empty">
				<p><strong>No cards yet.</strong></p>
				<p class="muted">
					Pick a Navidrome playlist or album{me.audiobookshelf.connected ? ', or an audiobook,' : ''} and it
					becomes a Yoto playlist you can link to a card.
				</p>
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
		padding: 1.25rem 16px 3rem;
	}
	.top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		margin-bottom: 1.25rem;
	}
	.brand {
		display: flex;
		align-items: center;
		gap: 0.6rem;
	}
	.brand h1 {
		font-size: 1.4rem;
		letter-spacing: -0.01em;
	}
	picture {
		display: contents;
	}
	.logo {
		width: 40px;
		height: 40px;
	}
	.who {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		font-size: 0.9rem;
	}
	.who form {
		margin: 0;
	}
	.status-row {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: center;
		margin-bottom: 1rem;
	}
	.pill {
		display: inline-flex;
		align-items: center;
		gap: 0.45em;
		padding: 0.3em 0.8em;
		border-radius: 999px;
		background: var(--surface);
		border: 1px solid var(--border);
		font-size: 0.875rem;
		font-weight: 600;
	}
	.pill .dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--muted);
	}
	.pill.ok .dot {
		background: var(--ok);
	}
	.pill.warn .dot {
		background: var(--warn);
	}
	.pill.bad .dot {
		background: var(--danger);
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
		margin: 1.5rem 0 0.9rem;
	}
	.cards-head h2 {
		font-size: 1.2rem;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(190px, 1fr));
		gap: 1rem;
	}
	.empty {
		text-align: center;
		padding: 3rem 1rem;
		border: 2px dashed var(--border);
		border-radius: var(--radius);
	}
	.empty p {
		margin: 0.25rem 0;
	}
	@media (max-width: 560px) {
		.banner {
			flex-direction: column;
			align-items: stretch;
		}
		.grid {
			grid-template-columns: repeat(2, 1fr);
			gap: 0.75rem;
		}
	}
</style>
