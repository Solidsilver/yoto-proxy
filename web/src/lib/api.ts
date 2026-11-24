export type Me = {
	name: string;
	user: string;
	yoto: { connected: boolean; label?: string; members?: string[] };
	navidromeOk: boolean;
	navidromeError: string;
	navidromeUser: string;
	audiobookshelf: {
		configured: boolean;
		connected: boolean;
		username?: string;
		kind?: 'api_key' | 'session';
	};
	authDisabled: boolean;
};

export type SourceType = 'navidrome_playlist' | 'navidrome_album' | 'abs_item';

export type Card = {
	id: number;
	title: string;
	sourceType: SourceType;
	sourceId: string;
	owner: string;
	yotoCardId: string;
	coverArtId: string;
	coverUrl: string;
	lastSyncedAt: string;
	lastError: string;
	createdAt: string;
	trackCount: number;
	/** Audiobook chapters being prepared for playback. */
	cache?: { done: number; total: number; running: boolean; error?: string };
};

export type CardTrack = {
	key: string;
	position: number;
	title: string;
	artist: string;
	duration: number;
};

export type Song = {
	id: string;
	title: string;
	artist: string;
	album: string;
	duration: number;
	suffix: string;
};

export type Playlist = {
	id: string;
	name: string;
	owner: string;
	songCount: number;
	duration: number;
	coverArt: string;
	entry?: Song[];
};

export type Album = {
	id: string;
	name: string;
	artist: string;
	year: number;
	songCount: number;
	duration: number;
	coverArt: string;
	song?: Song[];
};

export type ABSLibrary = { id: string; name: string; mediaType: string };

export type ABSItem = {
	id: string;
	media: {
		duration: number;
		numChapters: number;
		metadata: { title: string; authorName: string; seriesName?: string };
	};
};

export type ABSPlannedTrack = { title: string; duration: number; needsCache: boolean };

export type Invite = {
	label: string;
	invitedBy: string;
	members: string[];
	alreadyMember: boolean;
	inOtherYoto: boolean;
};

export class ApiError extends Error {
	constructor(
		public status: number,
		message: string
	) {
		super(message);
	}
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
	const res = await fetch(path, {
		method,
		headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
		body: body === undefined ? undefined : JSON.stringify(body)
	});
	if (res.status === 401) {
		location.href = '/auth/login?next=' + encodeURIComponent(location.pathname);
		throw new ApiError(401, 'Not logged in');
	}
	if (!res.ok) {
		let msg = res.statusText;
		try {
			msg = (await res.json()).error ?? msg;
		} catch {
			/* not JSON */
		}
		throw new ApiError(res.status, msg);
	}
	return res.status === 204 ? (undefined as T) : res.json();
}

export const api = {
	me: () => request<Me>('GET', '/api/me'),
	leaveYoto: () => request<void>('POST', '/api/yoto/leave'),
	renameYoto: (label: string) => request<void>('PATCH', '/api/yoto', { label }),
	createInvite: () => request<{ url: string; expiresAt: string }>('POST', '/api/yoto/invites'),
	invite: (token: string) => request<Invite>('GET', `/api/invites/${encodeURIComponent(token)}`),
	acceptInvite: (token: string) => request<void>('POST', `/api/invites/${encodeURIComponent(token)}/accept`),

	playlists: () => request<Playlist[]>('GET', '/api/navidrome/playlists'),
	playlist: (id: string) => request<Playlist>('GET', `/api/navidrome/playlists/${encodeURIComponent(id)}`),
	albums: (q = '', offset = 0, size = 60) =>
		request<Album[]>('GET', `/api/navidrome/albums?${new URLSearchParams({ q, offset: String(offset), size: String(size) })}`),
	album: (id: string) => request<Album>('GET', `/api/navidrome/albums/${encodeURIComponent(id)}`),

	disconnectAbs: () => request<void>('POST', '/api/abs/disconnect'),
	absLibraries: () => request<ABSLibrary[]>('GET', '/api/abs/libraries'),
	absItems: (libraryId: string, q = '', page = 0) =>
		request<{ items: ABSItem[]; total: number }>(
			'GET',
			`/api/abs/libraries/${encodeURIComponent(libraryId)}/items?${new URLSearchParams({ q, page: String(page) })}`
		),
	absItem: (id: string) =>
		request<{ item: ABSItem; tracks: ABSPlannedTrack[] }>('GET', `/api/abs/items/${encodeURIComponent(id)}`),

	cards: () => request<Card[]>('GET', '/api/cards'),
	card: (id: number) => request<{ card: Card; tracks: CardTrack[] }>('GET', `/api/cards/${id}`),
	createCard: (sourceType: SourceType, sourceId: string, title: string) =>
		request<Card>('POST', '/api/cards', { sourceType, sourceId, title }),
	renameCard: (id: number, title: string) => request<Card>('PATCH', `/api/cards/${id}`, { title }),
	syncCard: (id: number) => request<Card>('POST', `/api/cards/${id}/sync`),
	rotateToken: (id: number) => request<Card>('POST', `/api/cards/${id}/rotate-token`),
	deleteCard: (id: number, keepYoto = false) =>
		request<void>('DELETE', `/api/cards/${id}${keepYoto ? '?keepYoto=1' : ''}`)
};

export const coverSrc = (coverArt: string, size = 300) =>
	coverArt ? `/api/navidrome/cover/${encodeURIComponent(coverArt)}?size=${size}` : '';

/** The art as it will appear on the (portrait) Yoto card. */
export const portraitSrc = (coverArt: string) =>
	coverArt ? `/api/navidrome/cover/${encodeURIComponent(coverArt)}?portrait=1` : '';

export const absCoverSrc = (itemId: string, portrait = false) =>
	`/api/abs/cover/${encodeURIComponent(itemId)}?${portrait ? 'portrait=1' : 'size=120'}`;

export const cardCoverSrc = (card: Card) =>
	card.coverArtId ? `/api/cards/${card.id}/cover?v=${encodeURIComponent(card.coverArtId + card.lastSyncedAt)}` : '';

export function formatDuration(seconds: number): string {
	const h = Math.floor(seconds / 3600);
	const m = Math.floor((seconds % 3600) / 60);
	const s = seconds % 60;
	if (h) return `${h}h ${m}m`;
	return `${m}:${String(s).padStart(2, '0')}`;
}

export function timeAgo(iso: string): string {
	if (!iso) return 'never';
	const secs = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
	if (secs < 60) return 'just now';
	if (secs < 3600) return `${Math.floor(secs / 60)} min ago`;
	if (secs < 86400) return `${Math.floor(secs / 3600)} h ago`;
	return new Date(iso).toLocaleDateString();
}
