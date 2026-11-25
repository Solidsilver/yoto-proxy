<script lang="ts">
	import { api, cardCoverSrc, timeAgo, type Card } from '#lib/api.ts';

	type Props = {
		card: Card;
		onchange: (card: Card) => void;
		ondelete: (id: number) => void;
	};
	let { card, onchange, ondelete }: Props = $props();

	let busy = $state<'' | 'sync' | 'rename' | 'rotate' | 'delete'>('');
	let error = $state('');
	let more = $state<HTMLDetailsElement>();

	function onWindowClick(e: MouseEvent) {
		if (more?.open && !more.contains(e.target as Node)) more.open = false;
	}

	async function run(kind: typeof busy, fn: () => Promise<void>) {
		if (more) more.open = false;
		busy = kind;
		error = '';
		try {
			await fn();
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = '';
		}
	}

	const sync = () => run('sync', async () => onchange(await api.syncCard(card.id)));

	const rename = () =>
		run('rename', async () => {
			const title = prompt('New card title', card.title)?.trim();
			if (title && title !== card.title) onchange(await api.renameCard(card.id, title));
		});

	const rotate = () =>
		run('rotate', async () => {
			if (!confirm('Make new stream links for this card? The old links stop working immediately.')) return;
			onchange(await api.rotateToken(card.id));
		});

	const remove = () =>
		run('delete', async () => {
			if (!confirm(`Delete "${card.title}"? This also removes the playlist from your Yoto library.`)) return;
			await api.deleteCard(card.id);
			ondelete(card.id);
		});

	const sourceLabel = $derived(
		card.sourceType === 'abs_item' ? 'Audiobook' : card.sourceType === 'navidrome_album' ? 'Album' : 'Playlist'
	);
</script>

<svelte:window onclick={onWindowClick} />

<article class:has-error={!!card.lastError}>
	<div class="cover">
		{#if card.coverArtId}
			<img src={cardCoverSrc(card)} alt="" loading="lazy" />
		{:else}
			<span>♪</span>
		{/if}
	</div>
	<div class="body">
		<h3>{card.title}</h3>
		<p class="muted meta">
			{sourceLabel} · {card.trackCount} tracks{card.owner ? ` · ${card.owner}` : ''}
		</p>
		<p class="status">
			{#if !card.yotoCardId}
				<span class="dot warn"></span> Not on Yoto yet
			{:else if card.lastError}
				<span class="dot bad"></span> Last sync failed
			{:else}
				<span class="dot ok"></span> Synced {timeAgo(card.lastSyncedAt)}
			{/if}
		</p>
		{#if card.cache && card.cache.done < card.cache.total}
			<div class="prep" title="Chapters play right away; once prepared you can also skip within them.">
				<span class="muted">
					{card.cache.running
						? `Preparing chapters ${card.cache.done}/${card.cache.total}`
						: `${card.cache.done}/${card.cache.total} chapters prepared`}
				</span>
				<progress max={card.cache.total} value={card.cache.done}></progress>
			</div>
		{/if}
		{#if card.cache?.error}
			<p class="error">Preparing chapters: {card.cache.error}</p>
		{/if}
		{#if card.lastError || error}
			<p class="error">{error || card.lastError}</p>
		{/if}
		<div class="actions">
			<button class="btn small primary sync" onclick={sync} disabled={!!busy}>
				{busy === 'sync' ? 'Syncing…' : 'Sync now'}
			</button>
			<details bind:this={more}>
				<summary class="btn small more" aria-label="More actions">
					<svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"
						><circle cx="4.5" cy="10" r="1.8" fill="currentColor" /><circle cx="10" cy="10" r="1.8" fill="currentColor" /><circle
							cx="15.5"
							cy="10"
							r="1.8"
							fill="currentColor"
						/></svg
					>
				</summary>
				<div class="menu" role="menu">
					<button role="menuitem" onclick={rename} disabled={!!busy}>Rename</button>
					<button role="menuitem" onclick={rotate} disabled={!!busy}>New stream links</button>
					<button role="menuitem" class="danger" onclick={remove} disabled={!!busy}>Delete</button>
				</div>
			</details>
		</div>
	</div>
</article>

<style>
	article {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		box-shadow: var(--shadow);
		overflow: hidden;
		display: flex;
		flex-direction: column;
	}
	.cover {
		aspect-ratio: 5 / 8; /* Yoto cards are portrait */
		margin: 0.6rem 0.6rem 0;
		border-radius: 10px;
		overflow: hidden;
		background: var(--surface-2);
		display: grid;
		place-items: center;
		font-size: 3rem;
		color: var(--muted);
	}
	.cover img {
		width: 100%;
		height: 100%;
		object-fit: cover;
	}
	.body {
		padding: 0.85rem 0.9rem 0.9rem;
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		flex: 1;
	}
	h3 {
		font-size: 1.05rem;
		overflow-wrap: anywhere;
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}
	.meta,
	.status {
		margin: 0;
		font-size: 0.875rem;
	}
	.status {
		display: flex;
		align-items: center;
		gap: 0.4em;
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
	}
	.dot.ok {
		background: var(--ok);
	}
	.dot.warn {
		background: var(--warn);
	}
	.dot.bad {
		background: var(--danger);
	}
	.prep {
		font-size: 0.8rem;
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
	}
	.prep progress {
		width: 100%;
		height: 6px;
		accent-color: var(--accent);
	}
	.error {
		margin: 0.2rem 0 0;
		padding: 0.45rem 0.6rem;
		border-radius: 8px;
		font-size: 0.8rem;
		background: var(--danger-soft);
		color: var(--danger);
		overflow-wrap: anywhere;
	}
	.actions {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		margin-top: auto;
		padding-top: 0.5rem;
		position: relative;
	}
	.sync {
		flex: 1;
		min-height: 36px;
	}
	.more {
		width: 36px;
		height: 36px;
		padding: 0;
	}
	details[open] .more {
		background: var(--surface-2);
	}
	summary {
		list-style: none;
	}
	summary::-webkit-details-marker {
		display: none;
	}
	.menu {
		position: absolute;
		right: 0;
		bottom: calc(100% + 4px);
		display: flex;
		flex-direction: column;
		min-width: 190px;
		padding: 0.35rem;
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: 12px;
		box-shadow: var(--shadow);
		z-index: 2;
	}
	.menu button {
		border: 0;
		background: none;
		text-align: left;
		padding: 0.65rem 0.75rem;
		border-radius: 8px;
		font-weight: 600;
		cursor: pointer;
		white-space: nowrap;
	}
	.menu button:disabled {
		opacity: 0.55;
	}
	.menu button.danger {
		color: var(--danger);
	}
	@media (hover: hover) {
		.menu button:hover:not(:disabled) {
			background: var(--surface-2);
		}
	}
	@media (max-width: 560px) {
		.cover {
			margin: 0.45rem 0.45rem 0;
			border-radius: 8px;
		}
		.body {
			padding: 0.6rem 0.65rem 0.65rem;
			gap: 0.2rem;
		}
		h3 {
			font-size: 0.98rem;
		}
		.meta,
		.status {
			font-size: 0.8rem;
		}
		.menu {
			left: 0;
			min-width: 0;
		}
		.menu button {
			white-space: normal;
		}
		.meta {
			white-space: nowrap;
			overflow: hidden;
			text-overflow: ellipsis;
		}
	}
</style>
