<script lang="ts">
	import { api, type Me } from '#lib/api.ts';

	type Props = { me: Me; onchange: () => void };
	let { me, onchange }: Props = $props();

	let invite = $state<{ url: string; expiresAt: string } | null>(null);
	let busy = $state(false);
	let error = $state('');
	let copied = $state(false);

	const abs = $derived(me.audiobookshelf);
	const canShare = typeof navigator !== 'undefined' && 'share' in navigator;
	const others = $derived((me.yoto.members ?? []).filter((m) => m !== me.user));
	// Open by default only when something needs attention.
	const attention = $derived(!me.navidromeOk);
	let open = $state(false);
	$effect(() => {
		if (attention) open = true;
	});

	const summary = $derived.by(() => {
		const sources = ['Navidrome'];
		if (abs.connected) sources.push('Audiobookshelf');
		const shared = others.length ? `Shared with ${others.join(', ')}` : 'Just you';
		return `${shared} · ${sources.join(' & ')}`;
	});

	async function run(fn: () => Promise<void>) {
		busy = true;
		error = '';
		try {
			await fn();
		} catch (e) {
			error = (e as Error).message;
		} finally {
			busy = false;
		}
	}

	const makeInvite = () =>
		run(async () => {
			invite = await api.createInvite();
			copied = false;
			// On phones, go straight to the share sheet.
			if (canShare && matchMedia('(pointer: coarse)').matches) await share();
		});

	async function share() {
		if (!invite) return;
		try {
			await navigator.share({
				title: `Join “${me.yoto.label}” on Yoto Proxy`,
				text: `Make Yoto cards for “${me.yoto.label}” with me:`,
				url: invite.url
			});
		} catch {
			/* cancelled; the link stays visible below */
		}
	}

	async function copy() {
		if (!invite) return;
		try {
			await navigator.clipboard.writeText(invite.url);
			copied = true;
		} catch {
			/* the field is selectable */
		}
	}

	const rename = () =>
		run(async () => {
			const label = prompt('Name for this Yoto account', me.yoto.label)?.trim();
			if (!label || label === me.yoto.label) return;
			await api.renameYoto(label);
			onchange();
		});

	const leave = () =>
		run(async () => {
			const msg = others.length
				? `Leave “${me.yoto.label}”? ${others.join(', ')} keep${others.length === 1 ? 's' : ''} it and its cards.`
				: `Disconnect “${me.yoto.label}”? Its cards keep playing; connect the same Yoto account again to get them back.`;
			if (!confirm(msg)) return;
			await api.leaveYoto();
			onchange();
		});

	const disconnectAbs = () =>
		run(async () => {
			if (!confirm('Disconnect Audiobookshelf? Audiobook cards stop playing until you connect again.')) return;
			await api.disconnectAbs();
			onchange();
		});
</script>

<details class="conn" bind:open>
	<summary>
		<span class="icon" aria-hidden="true">
			<svg viewBox="0 0 24 24" width="22" height="22"
				><rect x="5" y="2.5" width="14" height="19" rx="3" fill="currentColor" opacity=".18" /><rect
					x="5"
					y="2.5"
					width="14"
					height="19"
					rx="3"
					fill="none"
					stroke="currentColor"
					stroke-width="1.8"
				/><circle cx="12" cy="15.5" r="2.2" fill="currentColor" /></svg
			>
		</span>
		<span class="sum-text">
			<span class="sum-title">{me.yoto.label}</span>
			<span class="sum-sub muted">{summary}</span>
		</span>
		<span class="dots" aria-label="Connection status">
			<span class="dot ok" title="Yoto connected"></span>
			<span class="dot" class:ok={me.navidromeOk} class:bad={!me.navidromeOk} title="Navidrome"></span>
			{#if abs.configured}
				<span class="dot" class:ok={abs.connected} title="Audiobookshelf"></span>
			{/if}
		</span>
		<svg class="chev" viewBox="0 0 20 20" width="18" height="18" aria-hidden="true"
			><path d="M5 7.5l5 5 5-5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg
		>
	</summary>

	<div class="rows">
		<div class="row stack">
			<div class="row-main">
				<span class="label">Yoto</span>
				<span class="value">
					{me.yoto.label}
					<span class="muted">·</span>
					<span class="muted">{others.length ? `with ${others.join(', ')}` : 'only you'}</span>
				</span>
			</div>
			<div class="row-actions">
				<button class="btn small primary" onclick={makeInvite} disabled={busy}>Invite</button>
				<button class="btn small" onclick={rename} disabled={busy}>Rename</button>
				<button class="btn small" onclick={leave} disabled={busy}>Leave</button>
			</div>
		</div>

		{#if invite}
			<div class="invite">
				<p class="muted">
					Anyone with a login here can join with this link. It works once and expires
					{new Date(invite.expiresAt).toLocaleDateString()}.
				</p>
				<div class="invite-row">
					<input type="text" readonly value={invite.url} onfocus={(e) => e.currentTarget.select()} />
					{#if canShare}
						<button class="btn small primary" onclick={share}>Share</button>
					{/if}
					<button class="btn small" onclick={copy}>{copied ? 'Copied' : 'Copy'}</button>
				</div>
			</div>
		{/if}

		<div class="row">
			<div class="row-main">
				<span class="label">Navidrome</span>
				{#if me.navidromeOk}
					<span class="value"><span class="dot ok"></span>{me.navidromeUser || 'Connected'}</span>
				{:else}
					<span class="value bad"><span class="dot bad"></span>{me.navidromeError || 'Unreachable'}</span>
				{/if}
			</div>
		</div>

		{#if abs.configured}
			<div class="row">
				<div class="row-main">
					<span class="label">Audiobookshelf</span>
					{#if abs.connected}
						<span class="value"><span class="dot ok"></span>{abs.username}</span>
					{:else}
						<span class="value muted">Not connected</span>
					{/if}
				</div>
				<div class="row-actions">
					{#if abs.connected}
						<button class="btn small" onclick={disconnectAbs} disabled={busy}>Disconnect</button>
					{:else}
						<a class="btn small primary" href="/auth/abs/login">Connect</a>
					{/if}
				</div>
			</div>
		{/if}

		{#if error}<p class="error">{error}</p>{/if}
	</div>
</details>

<style>
	.conn {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		margin-bottom: 1rem;
	}
	summary {
		list-style: none;
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.75rem 0.9rem;
		cursor: pointer;
		min-height: 56px;
		border-radius: var(--radius);
	}
	summary::-webkit-details-marker {
		display: none;
	}
	summary:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
	}
	.icon {
		display: grid;
		place-items: center;
		width: 38px;
		height: 38px;
		flex-shrink: 0;
		border-radius: 10px;
		background: var(--accent-soft);
		color: var(--accent);
	}
	.sum-text {
		display: flex;
		flex-direction: column;
		min-width: 0;
		flex: 1;
	}
	.sum-title {
		font-weight: 700;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.sum-sub {
		font-size: 0.825rem;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.dots {
		display: flex;
		gap: 4px;
	}
	.dot {
		display: inline-block;
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--muted);
		flex-shrink: 0;
	}
	.dot.ok {
		background: var(--ok);
	}
	.dot.bad {
		background: var(--danger);
	}
	.chev {
		color: var(--muted);
		transition: transform 0.2s;
		flex-shrink: 0;
	}
	details[open] .chev {
		transform: rotate(180deg);
	}
	.rows {
		border-top: 1px solid var(--border);
		padding: 0.25rem 0.9rem 0.75rem;
	}
	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem 1rem;
		flex-wrap: wrap;
		padding: 0.65rem 0;
		border-bottom: 1px solid var(--border);
	}
	.row:last-of-type {
		border-bottom: 0;
	}
	.row-main {
		display: flex;
		flex-direction: column;
		min-width: 0;
		gap: 0.1rem;
	}
	.label {
		font-size: 0.75rem;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--muted);
	}
	.value {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		flex-wrap: wrap;
		overflow-wrap: anywhere;
	}
	.value.bad {
		color: var(--danger);
	}
	.row-actions {
		display: flex;
		gap: 0.4rem;
		flex-wrap: wrap;
	}
	.invite {
		padding: 0.65rem 0;
		border-bottom: 1px solid var(--border);
	}
	.invite p {
		margin: 0 0 0.5rem;
		font-size: 0.85rem;
	}
	.invite-row {
		display: flex;
		gap: 0.4rem;
		align-items: center;
	}
	.invite-row input {
		min-width: 0;
		flex: 1;
		font-size: 16px; /* no iOS zoom on focus */
		padding: 0.4em 0.7em;
	}
	.error {
		margin: 0.5rem 0 0;
		color: var(--danger);
		font-size: 0.85rem;
	}
	@media (max-width: 560px) {
		.row.stack {
			flex-direction: column;
			align-items: stretch;
		}
		.stack .row-actions .btn {
			flex: 1;
		}
	}
</style>
