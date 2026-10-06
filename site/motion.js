// Motion for the Encore page (the same engine the other lilxcham.com project pages use, kept here so this
// site stands on its own). It only marks things; motion.css decides how they move.
//  - Parts get .in as they scroll into view and lose it once they have left, so they play again in both
//    scroll directions. Parts arriving together are staggered through --d (step from --stagger); --dir is
//    -1 for parts that left through the top, so they come back down into place when scrolling up.
//  - Reveals start once the opening veil lifts; any wheel, key or tap skips the opening.
//  - The mouse wheel glides instead of jumping. Touch, keyboard, scrollbar and links stay native.
(function () {
  var root = document.documentElement;
  root.setAttribute('data-reveal-ready', '');
  if (!root.classList.contains('motion')) return;

  var REVEAL = '.hero, .shot, section h2, section .sub, .card, .ticks li, .split img, .size, details, .final .wrap > *';
  var step = parseFloat(getComputedStyle(document.body).getPropertyValue('--stagger')) || .08;
  var io = new IntersectionObserver(function (entries) {
    var groups = [], counts = [];
    entries.forEach(function (e) {
      var el = e.target;
      if (!e.isIntersecting) {
        el.classList.remove('in');
        el.style.setProperty('--dir', e.boundingClientRect.top < 0 ? -1 : 1);
        return;
      }
      if (e.intersectionRatio < .12 || el.classList.contains('in')) return;
      var g = groups.indexOf(el.parentNode);
      if (g < 0) { groups.push(el.parentNode); counts.push(0); g = groups.length - 1; }
      el.style.setProperty('--d', (counts[g]++ * step).toFixed(2) + 's');
      el.classList.add('in');
    });
  }, { rootMargin: '0px 0px -8% 0px', threshold: [0, .12] });

  var revealing = false;
  function startReveals() {
    if (revealing) return; revealing = true;
    document.querySelectorAll(REVEAL).forEach(function (el) {
      // inside the hero only the hero itself and its screenshot are revealed; its text plays with the hero
      if (el.matches('.hero, .shot') || !el.closest('.hero')) io.observe(el);
    });
  }
  var intro = document.querySelector('.intro') ? 1.05 : 0; // seconds, matching the veil in motion.css
  var introTimer = setTimeout(startReveals, intro * 1000);
  var INPUT = ['wheel', 'keydown', 'pointerdown', 'touchstart'];
  function skipIntro() {
    INPUT.forEach(function (t) { removeEventListener(t, skipIntro); });
    if (revealing) return;
    root.classList.add('intro-skip'); clearTimeout(introTimer); startReveals();
  }
  if (intro) INPUT.forEach(function (t) { addEventListener(t, skipIntro, { passive: true }); });
})();

(function () {
  if (matchMedia('(pointer: coarse)').matches) return;
  var root = document.documentElement, target = scrollY, raf = 0, ours = false, lastStep = 0;
  function maxY() { return root.scrollHeight - innerHeight; }
  function canScroll(el, dy) { // let an inner scroll area take the wheel while it still can move that way
    for (; el && el !== document.body && el !== root; el = el.parentElement) {
      var oy = getComputedStyle(el).overflowY;
      if ((oy === 'auto' || oy === 'scroll') && el.scrollHeight > el.clientHeight + 1 &&
          (dy > 0 ? el.scrollTop + el.clientHeight < el.scrollHeight - 1 : el.scrollTop > 0)) return true;
    }
    return false;
  }
  function step(n) {
    // ease toward the target by elapsed time, not per frame, so the glide feels the same at any refresh rate
    var dt = lastStep && n ? Math.min(.05, (n - lastStep) / 1000) : 1 / 60; lastStep = n || 0;
    var y = scrollY, next = y + (target - y) * (1 - Math.pow(1 - .16, dt * 60));
    if (Math.abs(target - next) < .6) next = target;
    ours = true; scrollTo({ top: next, behavior: 'instant' });
    raf = next === target ? 0 : requestAnimationFrame(step);
    if (!raf) lastStep = 0;
  }
  addEventListener('wheel', function (e) {
    if (e.ctrlKey || Math.abs(e.deltaX) > Math.abs(e.deltaY) || canScroll(e.target, e.deltaY)) return;
    e.preventDefault();
    var dy = e.deltaY * (e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? innerHeight : 1);
    if (!raf) target = scrollY;
    target = Math.max(0, Math.min(maxY(), target + dy));
    if (!raf) raf = requestAnimationFrame(step);
  }, { passive: false });
  addEventListener('scroll', function () { if (ours) { ours = false; return; } if (!raf) target = scrollY; }, { passive: true });
  document.addEventListener('click', function (e) {
    if (e.target.closest && e.target.closest('a[href^="#"]') && raf) { cancelAnimationFrame(raf); raf = 0; lastStep = 0; }
  }, true);
})();
