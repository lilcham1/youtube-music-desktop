// Motion for the Encore page (the same engine the other lilxcham.com project pages use, kept here so this
// site stands on its own). It only marks things; motion.css decides how they move.
//  - Parts get .in as they scroll into view and lose it once they have left, so they play again in both
//    scroll directions. Parts arriving together are staggered through --d (step from --stagger); --dir is
//    -1 for parts that left through the top, so they come back down into place when scrolling up.
//  - Reveals start once the opening veil lifts; any wheel, key or tap skips the opening.
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
