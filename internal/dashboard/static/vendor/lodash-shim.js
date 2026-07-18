/* Minimal lodash.memoize + lodash.throttle shims for cytoscape-edgehandles UMD (no full lodash). */
(function (root) {
  function memoize(fn, resolver) {
    var cache = new Map();
    function memoized() {
      var key = resolver ? resolver.apply(this, arguments) : arguments[0];
      if (cache.has(key)) return cache.get(key);
      var result = fn.apply(this, arguments);
      cache.set(key, result);
      return result;
    }
    memoized.cache = cache;
    return memoized;
  }
  function throttle(fn, wait, options) {
    wait = wait || 0;
    options = options || {};
    var leading = options.leading !== false;
    var trailing = options.trailing !== false;
    var last = 0, timer = null, lastArgs = null, lastThis = null, result;
    function invoke() {
      last = Date.now();
      timer = null;
      result = fn.apply(lastThis, lastArgs);
      lastArgs = lastThis = null;
      return result;
    }
    function throttled() {
      var now = Date.now();
      if (!last && !leading) last = now;
      var remaining = wait - (now - last);
      lastArgs = arguments;
      lastThis = this;
      if (remaining <= 0 || remaining > wait) {
        if (timer) { clearTimeout(timer); timer = null; }
        return invoke();
      }
      if (!timer && trailing) timer = setTimeout(invoke, remaining);
      return result;
    }
    throttled.cancel = function () {
      if (timer) clearTimeout(timer);
      last = 0; timer = null; lastArgs = lastThis = null;
    };
    return throttled;
  }
  root._ = root._ || {};
  root._.memoize = memoize;
  root._.throttle = throttle;
})(typeof globalThis !== "undefined" ? globalThis : window);
