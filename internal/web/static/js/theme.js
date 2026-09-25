// Classic script: runs before the first paint so the chosen theme does not flash.
(function () {
    try {
        var t = localStorage.getItem('nu-theme');
        if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t;
    } catch (e) { /* storage blocked: follow the system theme */ }
})();
