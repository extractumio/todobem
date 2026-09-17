'use strict';
// grain.js — the surface grain (app.css, --grain) on a dense screen. grain.svg is a filter the
// browser evaluates per device pixel, so on a 2x screen its specks come out one device pixel
// wide: half the size they were drawn at, too fine to see, and the panel reads flat (measured:
// the same coverage in four times as many specks, three quarters of them one device pixel).
// Here the tile is rasterized once at one pixel per CSS pixel — the size it was drawn at — and
// scaled up to the screen's density without smoothing, every speck a k×k block, and the copies
// replace the SVG in --grain. They are blob URLs the page owns (img-src blob: in the CSP);
// nothing is stored, nothing leaves the page. A 1x screen keeps the SVG, and a window carried
// to a screen of another density is redone (a fractional density rounds: at 1.5x the 2x raster
// is laid down with the browser's own resampling). Nothing else in the page knows about this.

const Grain = (() => {
  // the two layers of --grain as app.css lays them: the tile, its offset and its size in CSS px
  const LAYERS = [
    { size: 512, at: '0 0' },
    { size: 571, at: '256px 128px' },
  ];
  let tile = null;
  let urls = [];

  // density is the screen's device pixels per CSS pixel, whole: the block a speck becomes
  const density = () => Math.round(window.devicePixelRatio || 1);

  // value composes the --grain property from one image URL per layer
  const value = images => LAYERS.map((l, i) => `url("${images[i]}") ${l.at}/${l.size}px ${l.size}px`).join(',');

  // load fetches grain.svg once, as an image the canvas can draw; a failed load is retried on
  // the next apply
  function load() {
    if (!tile) {
      tile = new Promise((resolve, reject) => {
        const img = new Image();
        img.onload = () => resolve(img);
        img.onerror = () => reject(new Error('grain.svg did not load'));
        img.src = 'grain.svg';
      });
      tile.catch(() => { tile = null; });
    }
    return tile;
  }

  // specked reports whether a drawn tile holds specks at all: a browser that draws an SVG
  // image into a canvas without its filter gives a blank raster, and the SVG must stay
  function specked(canvas) {
    const data = canvas.getContext('2d').getImageData(0, 0, canvas.width, canvas.height).data;
    let n = 0;
    for (let i = 3; i < data.length; i += 4 * 7) {
      if (data[i]) n++;
    }
    return n * 7 * 4 > data.length * 0.005;
  }

  // raster draws the tile at `size` CSS px (one pixel each, the SVG's own aliasing) and scales
  // that raster by k with no smoothing; the result is a blob URL
  function raster(img, size, k) {
    const base = document.createElement('canvas');
    base.width = size;
    base.height = size;
    base.getContext('2d').drawImage(img, 0, 0, size, size);
    if (!specked(base)) {
      return Promise.reject(new Error('the tile drew without its specks; keeping the SVG'));
    }
    const up = document.createElement('canvas');
    up.width = size * k;
    up.height = size * k;
    const ctx = up.getContext('2d');
    ctx.imageSmoothingEnabled = false;
    ctx.drawImage(base, 0, 0, up.width, up.height);
    return new Promise((resolve, reject) => {
      up.toBlob(blob => blob ? resolve(URL.createObjectURL(blob)) : reject(new Error('grain raster failed')), 'image/png');
    });
  }

  // release drops the rasters of the previous density
  function release(list) {
    for (const url of list) URL.revokeObjectURL(url);
  }

  // watch re-applies when the density changes: the query matches now and flips on any change
  function watch() {
    if (typeof window.matchMedia !== 'function') return;
    window.matchMedia(`(resolution: ${window.devicePixelRatio || 1}dppx)`).addEventListener('change', apply, { once: true });
  }

  function apply() {
    watch();
    const k = density();
    if (k < 2) {
      if (urls.length) {
        document.documentElement.style.removeProperty('--grain');
        release(urls);
        urls = [];
      }
      return;
    }
    load()
      .then(img => Promise.all(LAYERS.map(l => raster(img, l.size, k))))
      .then(images => {
        if (k !== density()) {
          release(images);
          return;
        }
        document.documentElement.style.setProperty('--grain', value(images));
        release(urls);
        urls = images;
      })
      .catch(e => console.warn('grain: ' + e.message));
  }

  apply();
  return { density, value };
})();
