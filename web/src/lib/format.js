/**
 * Format bytes to human-readable size.
 * @param {number} bytes
 */
export function formatSize(bytes) {
  if (!bytes) return '?';
  if (bytes >= 1e12) return (bytes / 1e12).toFixed(1) + '\u00a0TB';
  if (bytes >= 1e9) return (bytes / 1e9).toFixed(1) + '\u00a0GB';
  if (bytes >= 1e6) return (bytes / 1e6).toFixed(1) + '\u00a0MB';
  if (bytes >= 1e3) return (bytes / 1e3).toFixed(1) + '\u00a0KB';
  return bytes + '\u00a0B';
}

/**
 * Format a date string to relative or short date.
 * @param {string} dateStr
 */
export function formatDate(dateStr) {
  const d = new Date(dateStr);
  const diff = Math.floor((Date.now() - d.getTime()) / 1000);
  if (diff < 60) return 'just now';
  if (diff < 3600) return Math.floor(diff / 60) + 'm ago';
  if (diff < 86400) return Math.floor(diff / 3600) + 'h ago';
  if (diff < 7 * 86400) return Math.floor(diff / 86400) + 'd ago';
  return d.toLocaleDateString('nl-BE', { day: 'numeric', month: 'short', year: 'numeric' });
}

export const CATEGORIES = [
  { id: null, label: 'All', emoji: '⊞', color: 'text-slate-400', bg: 'bg-slate-700/40' },
  { id: 0, label: 'Video', emoji: '🎬', color: 'text-blue-400', bg: 'bg-blue-900/30' },
  { id: 1, label: 'Audio', emoji: '🎵', color: 'text-green-400', bg: 'bg-green-900/30' },
  { id: 2, label: 'Game', emoji: '🎮', color: 'text-purple-400', bg: 'bg-purple-900/30' },
  { id: 3, label: 'App', emoji: '💿', color: 'text-yellow-400', bg: 'bg-yellow-900/30' },
];

/** @param {number|null} id */
export function getCat(id) {
  return CATEGORIES.find(c => c.id === id) ?? CATEGORIES[0];
}

// Icon overrides keyed by [category][format/platform index from SubCatA]
const FORMAT_ICONS = {
  // Video formats (0.a.n)
  0: {
    3:  '📀',   // DVD5
    5:  '📖',   // ePub
    6:  '💿',   // Blu-ray
    10: '📀',   // DVD9
    11: '📄',   // PDF
    12: '🖼️',  // Bitmap/image
    14: '🥽',   // 3D
    15: '📺',   // UHD/4K
  },
  // Audio formats (1.a.n)
  1: {
    5: '🔊',   // DTS
    8: '🎼',   // FLAC
  },
  // Game platforms (2.a.n)
  2: {
    0:  '🖥️',  // PC
    13: '📱',   // Windows Phone
    14: '📱',   // iOS
    15: '📱',   // Android
  },
  // App platforms (3.a.n)
  3: {
    0: '🪟',   // Windows
    1: '🍎',   // Mac
    2: '🐧',   // Linux
    3: '💻',   // OS/2
    4: '📱',   // Windows Phone
    6: '📱',   // iOS
    7: '📱',   // Android
  },
};

/**
 * Returns the best emoji for a spot icon, using format/platform (SubCatA) when available.
 * Falls back to the main category emoji.
 * @param {object} spot
 * @returns {string}
 */
export function getFormatEmoji(spot) {
  const catIcons = FORMAT_ICONS[spot.Category];
  if (catIcons && spot.SubCatA) {
    const first = spot.SubCatA.split('|')[0];
    if (first) {
      const n = parseInt(first.slice(1));
      if (catIcons[n]) return catIcons[n];
    }
  }
  return getCat(spot.Category).emoji;
}

// Subcategory name maps keyed as "cat.slot.n"
const SUBCAT = {
  // Image a = format
  '0.a.0': 'DivX', '0.a.2': 'MPG', '0.a.3': 'DVD5',
  '0.a.5': 'ePub', '0.a.6': 'Blu-ray', '0.a.9': 'x264',
  '0.a.10': 'DVD9', '0.a.11': 'PDF', '0.a.12': 'Bitmap',
  '0.a.14': '3D', '0.a.15': 'UHD',
  // Image b = source
  '0.b.0': 'CAM', '0.b.3': 'Retail', '0.b.4': 'TV',
  '0.b.7': 'R5', '0.b.9': 'Telesync', '0.b.10': 'Scan',
  '0.b.11': 'WEB-DL', '0.b.12': 'WEBRip', '0.b.13': 'HDRip',
  // Image c = subtitles/language
  '0.c.0': 'No subs', '0.c.1': 'NL subs', '0.c.2': 'NL subs (hard)',
  '0.c.3': 'EN subs', '0.c.4': 'EN subs (hard)',
  '0.c.6': 'NL subs', '0.c.7': 'EN subs',
  '0.c.10': 'EN', '0.c.11': 'NL', '0.c.12': 'DE',
  '0.c.13': 'FR', '0.c.14': 'ES', '0.c.15': 'Asian',
  // Image d = genre
  '0.d.0': 'Action', '0.d.1': 'Adventure', '0.d.2': 'Animation',
  '0.d.3': 'Cabaret', '0.d.4': 'Comedy', '0.d.5': 'Crime',
  '0.d.6': 'Docu', '0.d.7': 'Drama', '0.d.8': 'Family',
  '0.d.9': 'Fantasy', '0.d.11': 'TV', '0.d.12': 'Horror',
  '0.d.14': 'Musical', '0.d.15': 'Mystery', '0.d.16': 'Romance',
  '0.d.17': 'Sci-Fi', '0.d.18': 'Sport', '0.d.20': 'Thriller',
  '0.d.21': 'War', '0.d.22': 'Western', '0.d.28': 'Asian',
  '0.d.29': 'Anime', '0.d.30': 'Cover', '0.d.31': 'Comics',
  // Audio a = format
  '1.a.0': 'MP3', '1.a.1': 'WMA', '1.a.2': 'WAV', '1.a.3': 'OGG',
  '1.a.4': 'EAC', '1.a.5': 'DTS', '1.a.6': 'AAC', '1.a.7': 'APE', '1.a.8': 'FLAC',
  // Game a = platform
  '2.a.0': 'PC', '2.a.3': 'PS', '2.a.4': 'PS2', '2.a.5': 'PSP',
  '2.a.6': 'XBox', '2.a.7': 'X360', '2.a.10': 'NDS', '2.a.11': 'Wii',
  '2.a.12': 'PS3', '2.a.13': 'WPhone', '2.a.14': 'iOS', '2.a.15': 'Android',
  '2.a.16': '3DS', '2.a.17': 'PS4', '2.a.18': 'XB1',
  // App a = platform
  '3.a.0': 'Windows', '3.a.1': 'Mac', '3.a.2': 'Linux', '3.a.3': 'OS/2',
  '3.a.4': 'WPhone', '3.a.6': 'iOS', '3.a.7': 'Android',
};

/**
 * Parse a pipe-delimited subcat string into readable labels.
 * @param {string} str e.g. "a9|a15|"
 * @param {number} cat  main category id
 * @param {string} slot 'a'|'b'|'c'|'d'
 * @returns {string[]}
 */
export function parseSubCat(str, cat, slot) {
  if (!str) return [];
  return str.split('|').filter(Boolean).map(s => {
    const n = parseInt(s.slice(1));
    return SUBCAT[`${cat}.${slot}.${n}`] ?? null;
  }).filter(Boolean);
}

// Section labels per category slot
const SLOT_LABELS = {
  '0.a': 'Format', '0.b': 'Source', '0.c': 'Language', '0.d': 'Genre',
  '1.a': 'Format', '1.b': 'Genre',
  '2.a': 'Platform', '2.b': 'Genre',
  '3.a': 'Platform', '3.b': 'Genre',
};

/**
 * Returns an array of { label, values } sections for the detail view.
 * @param {object} spot
 * @returns {{ label: string, values: string[] }[]}
 */
export function parseSubCatSections(spot) {
  const cat = spot.Category;
  const slots = [
    { key: 'a', str: spot.SubCatA },
    { key: 'b', str: spot.SubCatB },
    { key: 'c', str: spot.SubCatC },
    { key: 'd', str: spot.SubCatD },
  ];
  const out = [];
  for (const { key, str } of slots) {
    const values = parseSubCat(str, cat, key);
    if (values.length) {
      out.push({ label: SLOT_LABELS[`${cat}.${key}`] ?? key.toUpperCase(), values });
    }
  }
  return out;
}

/**
 * Convert Spotnet BBCode to safe HTML.
 * Supported tags: [b], [i], [u], [s], [br], [color=x], [url=x], [url], [img], [list], [*]
 * All other tags are stripped. HTML special chars are escaped first.
 * @param {string} text
 * @returns {string} HTML string
 */
export function renderBBCode(text) {
  if (!text) return '';

  // Escape HTML special chars first
  let s = text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');

  // Newlines and [br]
  s = s.replace(/\[br\]/gi, '\n');
  s = s.replace(/\r?\n/g, '<br>');

  // Simple inline tags
  s = s.replace(/\[b\]([\s\S]*?)\[\/b\]/gi, '<strong>$1</strong>');
  s = s.replace(/\[i\]([\s\S]*?)\[\/i\]/gi, '<em>$1</em>');
  s = s.replace(/\[u\]([\s\S]*?)\[\/u\]/gi, '<u>$1</u>');
  s = s.replace(/\[s\]([\s\S]*?)\[\/s\]/gi, '<s>$1</s>');

  // Color — only allow named colors and hex to avoid injection
  s = s.replace(/\[color=["']?(#?[a-zA-Z0-9]+)["']?\]([\s\S]*?)\[\/color\]/gi, (_, color, content) => {
    // Whitelist: hex colors and a safe set of named colors
    if (/^(#[0-9a-fA-F]{3,6}|red|green|blue|yellow|orange|purple|white|black|gray|grey|lime|teal|cyan|pink|brown|navy|maroon)$/i.test(color)) {
      return `<span style="color:${color}">${content}</span>`;
    }
    return content;
  });

  // [url=href]label[/url]
  s = s.replace(/\[url=["']?(https?:\/\/[^\s"'\]]+)["']?\]([\s\S]*?)\[\/url\]/gi,
    '<a href="$1" target="_blank" rel="noopener noreferrer" class="text-blue-400 underline">$2</a>');
  // [url]href[/url]
  s = s.replace(/\[url\](https?:\/\/[^\s\]]+)\[\/url\]/gi,
    '<a href="$1" target="_blank" rel="noopener noreferrer" class="text-blue-400 underline">$1</a>');

  // [img] — skip, don't render arbitrary images
  s = s.replace(/\[img\][\s\S]*?\[\/img\]/gi, '');

  // [list] / [*]
  s = s.replace(/\[list\]([\s\S]*?)\[\/list\]/gi, (_, inner) => {
    const items = inner.split(/\[\*\]/).filter(i => i.trim());
    return '<ul class="list-disc list-inside my-1">' + items.map(i => `<li>${i.trim()}</li>`).join('') + '</ul>';
  });

  // Strip any remaining unrecognized [tags]
  s = s.replace(/\[[^\]]{0,30}\]/g, '');

  return s;
}
