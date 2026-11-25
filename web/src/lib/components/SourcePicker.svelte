<script lang="ts">
	import {
		api,
		absCoverSrc,
		coverSrc,
		portraitSrc,
		formatDuration,
		type ABSItem,
		type ABSLibrary,
		type Album,
		type Playlist,
		type SourceType
	} from '#lib/api.ts';

	type Props = {
		absConnected: boolean;
		onclose: () => void;
		oncreate: (sourceType: SourceType, sourceId: string, title: string) => Promise<void>;
	};
	let { absConnected, onclose, oncreate }: Props = $props();

	type PreviewTrack = { title: string; artist?: string; duration: number };
	type Selection = {
		type: SourceType;
		id: string;
		name: string;
		subtitle: string;
		portrait: string; // card art preview URL
		tracks: PreviewTrack[] | null;
		cutCount: number; // tracks that must be prepared (audiobook chapters)
	};

	let tab = $state<'playlists' | 'albums' | 'books'>('playlists');
	let query = $state('');
	let playlists = $state<Playlist[] | null>(null);
	let albums = $state<Album[] | null>(null);
	let libraries = $state<ABSLibrary[] | null>(null);
	let libraryId = $state('');
	let books = $state<ABSItem[] | null>(null);
	let error = $state('');
	let selected = $state<Selection | null>(null);
	let title = $state('');
	let creating = $state(false);
	let dialog: HTMLDialogElement;

	$effect(() => {
		dialog.showModal();
		api.playlists().then(
			(p) => {
				playlists = p;
				// Nothing to pick here: start on albums instead.
				if (!p.length && tab === 'playlists') tab = 'albums';
			},
			(e) => (error = e.message)
		);
	});

	// Debounced album search.
	$effect(() => {
		if (tab !== 'albums') return;
		const q = query;
		const t = setTimeout(() => {
			api.albums(q.trim()).then((a) => (albums = a), (e) => (error = e.message));
		}, 250);
		return () => clearTimeout(t);
	});

	// Audiobookshelf: load libraries once, then list/search the chosen one.
	$effect(() => {
		if (tab !== 'books' || libraries !== null) return;
		api.absLibraries().then(
			(libs) => {
				libraries = libs;
				libraryId = libs[0]?.id ?? '';
				if (!libs.length) books = [];
			},
			(e) => (error = e.message)
		);
	});
	$effect(() => {
		if (tab !== 'books' || !libraryId) return;
		const q = query;
		const lib = libraryId;
		books = null;
		const t = setTimeout(() => {
			api.absItems(lib, q.trim()).then(
				(r) => {
					if (lib === libraryId) books = r.items;
				},
				(e) => (error = e.message)
			);
		}, 250);
		return () => clearTimeout(t);
	});

	const filteredPlaylists = $derived(
		(playlists ?? []).filter((p) => p.name.toLowerCase().includes(query.trim().toLowerCase()))
	);

	function setTab(t: typeof tab) {
		tab = t;
		error = '';
	}

	async function pick(sel: Omit<Selection, 'tracks' | 'cutCount'>) {
		selected = { ...sel, tracks: null, cutCount: 0 };
		title = sel.name;
		error = '';
		try {
			let tracks: PreviewTrack[];
			let cutCount = 0;
			if (sel.type === 'navidrome_playlist') {
				tracks = (await api.playlist(sel.id)).entry ?? [];
			} else if (sel.type === 'navidrome_album') {
				tracks = (await api.album(sel.id)).song ?? [];
			} else {
				const r = await api.absItem(sel.id);
				tracks = r.tracks.map((t) => ({ title: t.title, duration: Math.round(t.duration) }));
				cutCount = r.tracks.filter((t) => t.needsCache).length;
			}
			if (selected?.id === sel.id) {
				selected.tracks = tracks;
				selected.cutCount = cutCount;
			}
		} catch (e) {
			error = (e as Error).message;
		}
	}

	async function create() {
		if (!selected) return;
		creating = true;
		error = '';
		try {
			await oncreate(selected.type, selected.id, title.trim());
			dialog.close();
		} catch (e) {
			error = (e as Error).message;
		} finally {
			creating = false;
		}
	}
</script>

<dialog bind:this={dialog} {onclose} aria-labelledby="picker-title">
	<header>
		{#if selected}
			<button class="btn small" onclick={() => (selected = null)}>← Back</button>
		{/if}
		<h2 id="picker-title">{selected ? 'New card' : 'Put on a card'}</h2>
		<button class="btn close" aria-label="Close" onclick={() => dialog.close()}>
			<svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"
				><path d="M5 5l10 10M15 5L5 15" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" /></svg
			>
		</button>
	</header>

	{#if error}<p class="error">{error}</p>{/if}

	{#if selected}
		<div class="preview">
			<div class="preview-head">
				{#if selected.portrait}
					<img src={selected.portrait} alt="" title="How the card art will look" />
				{:else}
					<div class="cover-placeholder">♪</div>
				{/if}
				<div class="preview-meta">
					<label>
						<span class="muted">Card title (shown in the Yoto app)</span>
						<input type="text" bind:value={title} maxlength="100" />
					</label>
					<p class="muted">{selected.subtitle}</p>
					{#if selected.cutCount > 0}
						<p class="note">
							{selected.cutCount} chapter{selected.cutCount === 1 ? '' : 's'} will be prepared for the
							player in the background. They play right away, and gain skipping within a chapter once
							ready.
						</p>
					{/if}
				</div>
			</div>
			<ol class="songs">
				{#if selected.tracks === null}
					<li class="muted">Loading tracks…</li>
				{:else if selected.tracks.length === 0}
					<li class="muted">This has no tracks.</li>
				{:else}
					{#each selected.tracks as track, i (i)}
						<li>
							<span class="song-title">{track.title}</span>
							{#if track.artist}<span class="muted">{track.artist}</span>{/if}
							<span class="muted dur">{formatDuration(track.duration)}</span>
						</li>
					{/each}
				{/if}
			</ol>
		</div>
		<footer>
			<button
				class="btn primary create"
				disabled={creating || !title.trim() || !selected.tracks?.length}
				onclick={create}
			>
				{creating ? 'Sending to Yoto…' : 'Create card'}
			</button>
		</footer>
	{:else}
		<div class="controls">
			<div class="tabs" role="tablist">
				<button role="tab" aria-selected={tab === 'playlists'} onclick={() => setTab('playlists')}>
					Playlists
				</button>
				<button role="tab" aria-selected={tab === 'albums'} onclick={() => setTab('albums')}>
					Albums
				</button>
				{#if absConnected}
					<button role="tab" aria-selected={tab === 'books'} onclick={() => setTab('books')}>
						Audiobooks
					</button>
				{/if}
			</div>
			<div class="search-row">
				{#if tab === 'books' && libraries && libraries.length > 1}
					<select bind:value={libraryId} aria-label="Library">
						{#each libraries as lib (lib.id)}
							<option value={lib.id}>{lib.name}</option>
						{/each}
					</select>
				{/if}
				<input
					type="search"
					placeholder={tab === 'albums'
						? 'Search albums or artists'
						: tab === 'books'
							? 'Search titles, authors or series'
							: 'Filter playlists'}
					bind:value={query}
				/>
			</div>
		</div>

		<ul class="results">
			{#if tab === 'playlists'}
				{#if playlists === null}
					<li class="muted pad">Loading playlists…</li>
				{:else if filteredPlaylists.length === 0}
					<li class="muted pad">No playlists. Make one in Navidrome, or ask someone to make theirs public.</li>
				{/if}
				{#each filteredPlaylists as p (p.id)}
					<li>
						<button
							onclick={() =>
								pick({
									type: 'navidrome_playlist',
									id: p.id,
									name: p.name,
									subtitle: `${p.songCount} tracks · ${formatDuration(p.duration)} · by ${p.owner}`,
									portrait: portraitSrc(p.coverArt)
								})}
						>
							<img src={coverSrc(p.coverArt, 120)} alt="" loading="lazy" />
							<span class="name">{p.name}</span>
							<span class="muted">{p.songCount} tracks · {formatDuration(p.duration)}</span>
						</button>
					</li>
				{/each}
			{:else if tab === 'albums'}
				{#if albums === null}
					<li class="muted pad">Loading albums…</li>
				{:else if albums.length === 0}
					<li class="muted pad">No albums found.</li>
				{/if}
				{#each albums ?? [] as a (a.id)}
					<li>
						<button
							onclick={() =>
								pick({
									type: 'navidrome_album',
									id: a.id,
									name: a.name,
									subtitle: `${a.artist}${a.year ? ` · ${a.year}` : ''} · ${a.songCount} tracks`,
									portrait: portraitSrc(a.coverArt)
								})}
						>
							<img src={coverSrc(a.coverArt, 120)} alt="" loading="lazy" />
							<span class="name">{a.name}</span>
							<span class="muted">{a.artist}{a.year ? ` · ${a.year}` : ''}</span>
						</button>
					</li>
				{/each}
			{:else}
				{#if books === null}
					<li class="muted pad">Loading audiobooks…</li>
				{:else if books.length === 0}
					<li class="muted pad">No audiobooks found.</li>
				{/if}
				{#each books ?? [] as b (b.id)}
					{@const md = b.media.metadata}
					<li>
						<button
							onclick={() =>
								pick({
									type: 'abs_item',
									id: b.id,
									name: md.title,
									subtitle: [md.authorName, md.seriesName, formatDuration(Math.round(b.media.duration))]
										.filter(Boolean)
										.join(' · '),
									portrait: absCoverSrc(b.id, true)
								})}
						>
							<img src={absCoverSrc(b.id)} alt="" loading="lazy" class="book" />
							<span class="name">{md.title}</span>
							<span class="muted"
								>{[md.authorName, formatDuration(Math.round(b.media.duration))].filter(Boolean).join(' · ')}</span
							>
						</button>
					</li>
				{/each}
			{/if}
		</ul>
	{/if}
</dialog>

<style>
	dialog {
		width: min(640px, calc(100vw - 24px));
		/* A definite height: WebKit collapses flex: 1 scroll areas inside a
		   dialog that only has a max-height. */
		height: min(720px, calc(100dvh - 24px));
		padding: 0;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: var(--surface);
		color: var(--text);
		box-shadow: var(--shadow);
		display: flex;
		flex-direction: column;
	}
	dialog:not([open]) {
		display: none;
	}
	dialog::backdrop {
		background: rgb(20 12 6 / 0.45);
	}
	header {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 1rem 1rem 0.75rem;
	}
	.close {
		width: 36px;
		height: 36px;
		padding: 0;
		flex-shrink: 0;
	}
	header h2 {
		font-size: 1.1rem;
		flex: 1;
	}
	.controls {
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
		padding: 0 1rem 0.75rem;
		border-bottom: 1px solid var(--border);
	}
	.tabs {
		display: flex;
		gap: 0.25rem;
		background: var(--surface-2);
		padding: 0.25rem;
		border-radius: 999px;
		align-self: flex-start;
	}
	.tabs button {
		border: 0;
		background: transparent;
		padding: 0.35em 1em;
		border-radius: 999px;
		cursor: pointer;
		font-weight: 600;
		color: var(--muted);
	}
	.tabs button[aria-selected='true'] {
		background: var(--surface);
		color: var(--text);
		box-shadow: var(--shadow);
	}
	.results {
		list-style: none;
		margin: 0;
		padding: 0.5rem;
		overflow-y: auto;
		flex: 1 1 auto;
		min-height: 0;
	}
	.results button {
		display: grid;
		grid-template-columns: 52px minmax(0, 1fr);
		grid-template-rows: auto auto;
		column-gap: 0.75rem;
		align-items: center;
		width: 100%;
		text-align: left;
		padding: 0.5rem;
		border: 0;
		border-radius: 10px;
		background: transparent;
		cursor: pointer;
	}
	.results button:active {
		background: var(--surface-2);
	}
	@media (hover: hover) {
		.results button:hover {
			background: var(--surface-2);
		}
	}
	.results img {
		grid-row: span 2;
		width: 52px;
		height: 52px;
		border-radius: 8px;
		object-fit: cover;
		background: var(--surface-2);
	}
	.results img.book {
		object-fit: contain;
	}
	.search-row {
		display: flex;
		gap: 0.5rem;
	}
	.search-row select {
		font: inherit;
		color: inherit;
		padding: 0.5em 0.7em;
		border-radius: 10px;
		border: 1px solid var(--border);
		background: var(--surface);
		max-width: 40%;
	}
	.note {
		margin: 0.5rem 0 0;
		padding: 0.5rem 0.65rem;
		border-radius: 8px;
		background: var(--accent-soft);
		font-size: 0.85rem;
	}
	.results .name {
		font-weight: 600;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.results .muted {
		font-size: 0.875rem;
	}
	.results button .muted {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.pad {
		padding: 1rem;
	}
	.preview {
		overflow-y: auto;
		flex: 1 1 auto;
		min-height: 0;
		padding: 0 1rem;
	}
	.preview-head {
		display: flex;
		gap: 1rem;
		align-items: flex-start;
		margin-bottom: 0.75rem;
	}
	.preview-head img,
	.cover-placeholder {
		width: 100px;
		height: 160px;
		border-radius: 12px;
		object-fit: cover;
		flex-shrink: 0;
		background: var(--surface-2);
	}
	.cover-placeholder {
		display: grid;
		place-items: center;
		font-size: 2.5rem;
		color: var(--muted);
	}
	.preview-meta {
		flex: 1;
		min-width: 0;
	}
	.preview-meta label {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		font-size: 0.875rem;
	}
	.songs {
		margin: 0;
		padding: 0 0 0 1.5rem;
		font-size: 0.925rem;
	}
	.songs li {
		padding: 0.3rem 0;
		border-bottom: 1px solid var(--border);
	}
	.songs li:last-child {
		border-bottom: 0;
	}
	.song-title {
		margin-right: 0.4em;
	}
	.dur {
		float: right;
		font-variant-numeric: tabular-nums;
	}
	footer {
		padding: 0.75rem 1rem 1rem;
		border-top: 1px solid var(--border);
		display: flex;
		justify-content: flex-end;
	}
	.error {
		margin: 0 1rem 0.75rem;
		padding: 0.6rem 0.8rem;
		border-radius: 10px;
		background: var(--danger-soft);
		color: var(--danger);
	}
	@media (max-width: 560px) {
		/* Full-screen sheet on phones. */
		dialog {
			width: 100%;
			max-width: 100%;
			height: 100dvh;
			max-height: 100dvh;
			margin: 0;
			border: 0;
			border-radius: 0;
			box-shadow: none;
		}
		header {
			padding: calc(0.75rem + env(safe-area-inset-top)) calc(1rem + env(safe-area-inset-right)) 0.6rem
				calc(1rem + env(safe-area-inset-left));
		}
		.tabs {
			align-self: stretch;
		}
		.tabs button {
			flex: 1;
			padding: 0.5em 0.5em;
		}
		.results {
			padding-bottom: calc(0.5rem + env(safe-area-inset-bottom));
		}
		.preview-head img,
		.cover-placeholder {
			width: 84px;
			height: 134px;
		}
		footer {
			padding-bottom: calc(0.75rem + env(safe-area-inset-bottom));
		}
		.create {
			width: 100%;
			padding: 0.75em 1em;
		}
	}
</style>
